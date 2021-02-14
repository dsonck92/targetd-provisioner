package iscsi

import (
	"context"
	"errors"
	"fmt"
	"go.uber.org/zap"
	"sort"
	"strconv"
	"strings"

	"github.com/magiconair/properties"
	"github.com/powerman/rpc-codec/jsonrpc2"
	"github.com/spf13/viper"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v6/controller"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v6/util"
)

type chapSessionCredentials struct {
	InUser      string `properties:"node.session.auth.username"`
	InPassword  string `properties:"node.session.auth.password"`
	OutUser     string `properties:"node.session.auth.username_in"`
	OutPassword string `properties:"node.session.auth.password_in"`
}

type volCreateArgs struct {
	Pool string `json:"pool"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

//initiator_set_auth(initiator_wwn, in_user, in_pass, out_user, out_pass)
type initiatorSetAuthArgs struct {
	InitiatorWwn string `json:"initiator_wwn"`
	InUser       string `json:"in_user"`
	InPassword   string `json:"in_pass"`
	OutUser      string `json:"out_user"`
	OutPassword  string `json:"out_pass"`
}

type volDestroyArgs struct {
	Pool string `json:"pool"`
	Name string `json:"name"`
}

type exportCreateArgs struct {
	Pool         string `json:"pool"`
	Vol          string `json:"vol"`
	InitiatorWwn string `json:"initiator_wwn"`
	Lun          int32  `json:"lun"`
}

type exportDestroyArgs struct {
	Pool         string `json:"pool"`
	Vol          string `json:"vol"`
	InitiatorWwn string `json:"initiator_wwn"`
}

type iscsiProvisioner struct {
	targetdURL string
	log *zap.Logger
}

type export struct {
	InitiatorWwn string `json:"initiator_wwn"`
	Lun          int32  `json:"lun"`
	VolName      string `json:"vol_name"`
	VolSize      int    `json:"vol_size"`
	VolUUID      string `json:"vol_uuid"`
	Pool         string `json:"pool"`
}

type exportList []export

func (l exportList) String() string {
	return fmt.Sprint((interface{})(l))
}

// NewiscsiProvisioner creates new iscsi provisioner
func NewiscsiProvisioner(url string, logger *zap.Logger) controller.Provisioner {
	return &iscsiProvisioner{
		targetdURL: url,
		log: logger.With(zap.String("system", "iscsi")),
	}
}

// getAccessModes returns access modes iscsi volume supported.
func (p *iscsiProvisioner) getAccessModes() []v1.PersistentVolumeAccessMode {
	return []v1.PersistentVolumeAccessMode{
		v1.ReadWriteOnce,
		v1.ReadOnlyMany,
	}
}

// Provision creates a storage asset and returns a PV object representing it.
func (p *iscsiProvisioner) Provision(context context.Context, options controller.ProvisionOptions) (*v1.PersistentVolume, controller.ProvisioningState, error) {
	log := p.log.With(zap.String("name",options.PVName))
	if !util.AccessModesContainedInAll(p.getAccessModes(), options.PVC.Spec.AccessModes) {
		return nil, controller.ProvisioningNoChange, fmt.Errorf("invalid AccessModes %v: only AccessModes %v are supported", options.PVC.Spec.AccessModes, p.getAccessModes())
	}
	log.Debug("new provision request received for pvc")
	vol, lun, pool, err := p.createVolume(options)
	if err != nil {
		log.Warn("failed to create volume", zap.Error(err))
		return nil, controller.ProvisioningNoChange, err
	}
	log.Debug("volume created", zap.String("vol",vol), zap.Int32("lun",lun))

	annotations := make(map[string]string)
	annotations["volume_name"] = vol
	annotations["pool"] = pool
	annotations["initiators"] = options.StorageClass.Parameters["initiators"]

	var portals []string
	if len(options.StorageClass.Parameters["portals"]) > 0 {
		portals = strings.Split(options.StorageClass.Parameters["portals"], ",")
	}

	pv := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        options.PVName,
			Labels:      map[string]string{},
			Annotations: annotations,
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: *options.StorageClass.ReclaimPolicy,
			AccessModes:                   options.PVC.Spec.AccessModes,
			Capacity: v1.ResourceList{
				v1.ResourceName(v1.ResourceStorage): options.PVC.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)],
			},
			// set volumeMode from PVC Spec
			VolumeMode: options.PVC.Spec.VolumeMode,
			PersistentVolumeSource: v1.PersistentVolumeSource{
				ISCSI: &v1.ISCSIPersistentVolumeSource{
					TargetPortal:      options.StorageClass.Parameters["targetPortal"],
					Portals:           portals,
					IQN:               options.StorageClass.Parameters["iqn"],
					ISCSIInterface:    options.StorageClass.Parameters["iscsiInterface"],
					Lun:               lun,
					ReadOnly:          getReadOnly(options.StorageClass.Parameters["readonly"]),
					FSType:            getFsType(options.StorageClass.Parameters["fsType"]),
					DiscoveryCHAPAuth: getBool(options.StorageClass.Parameters["chapAuthDiscovery"]),
					SessionCHAPAuth:   getBool(options.StorageClass.Parameters["chapAuthSession"]),
					SecretRef:         getSecretRef(getBool(options.StorageClass.Parameters["chapAuthDiscovery"]), getBool(options.StorageClass.Parameters["chapAuthSession"]), &v1.SecretReference{Name: viper.GetString("provisioner-name") + "-chap-secret"}),
				},
			},
		},
	}
	return pv, controller.ProvisioningFinished, nil
}

func getReadOnly(readonly string) bool {
	isReadOnly, err := strconv.ParseBool(readonly)
	if err != nil {
		return false
	}
	return isReadOnly
}

func getFsType(fsType string) string {
	if fsType == "" {
		return viper.GetString("default-fs")
	}
	return fsType
}

func getSecretRef(discovery bool, session bool, ref *v1.SecretReference) *v1.SecretReference {
	if discovery || session {
		return ref
	}
	return nil
}

func getBool(value string) bool {
	res, err := strconv.ParseBool(value)
	if err != nil {
		return false
	}
	return res

}

// Delete removes the storage asset that was created by Provision represented
// by the given PV.
func (p *iscsiProvisioner) Delete(context context.Context, volume *v1.PersistentVolume) error {
	log := p.log.With(zap.String("name", volume.GetName()))
	//vol from the annotation
	log.Debug("volume deletion request received")
	{
		log := log.With(zap.String("vol", volume.Annotations["volume_name"]), zap.String("pool", volume.Annotations["pool"]))
		for _, initiator := range strings.Split(volume.Annotations["initiators"], ",") {
			log := log.With(zap.String("initiator", initiator))
			log.Debug("removing iscsi export")
			err := p.exportDestroy(volume.Annotations["volume_name"], volume.Annotations["pool"], initiator)
			if err != nil {
				log.Warn("failed to destroy iscsi export", zap.Error(err))
				return err
			}
			log.Debug("iscsi export removed")
		}
		log.Debug("removing logical volume")
		err := p.volDestroy(volume.Annotations["volume_name"], volume.Annotations["pool"])
		if err != nil {
			log.Warn("failed to remove logical volume", zap.Error(err))
			return err
		}
		log.Debug("logical volume removed")
	}
	log.Debug("volume deletion request completed")
	return nil
}

func (p *iscsiProvisioner) createVolume(options controller.ProvisionOptions) (vol string, lun int32, pool string, err error) {
	size := getSize(options)
	vol = p.getVolumeName(options)
	pool = p.getVolumeGroup(options)
	initiators := p.getInitiators(options)
	chapCredentials := &chapSessionCredentials{}
	log := p.log
	//read chap session authentication credentials
	if getBool(options.StorageClass.Parameters["chapAuthSession"]) {
		prop, err2 := properties.LoadFile(viper.GetString("session-chap-credential-file-path"), properties.UTF8)
		if err2 != nil {
			p.log.Warn("failed to load chap credentials", zap.Error(err2))
			return "", 0, "", err2
		}
		err2 = prop.Decode(chapCredentials)
		if err2 != nil {
			p.log.Warn("failed to decode chap credentials", zap.Error(err2))
			return "", 0, "", err2
		}
	}

	p.log.Debug("calling export_list")
	exportList1, err := p.exportList()
	if err != nil {
		log.Warn("failed to get export_list", zap.Error(err))
		return "", 0, "", err
	}
	log.Debug("export_list called")
	lun, err = p.getFirstAvailableLun(exportList1)
	if err != nil {
		log.Warn("failed to get first available lun", zap.Error(err))
		return "", 0, "", err
	}
	{
		log := log.With(zap.String("vol", vol), zap.Int64("size", size), zap.String("pool", pool))
		log.Debug("creating volume")
		err = p.volCreate(vol, size, pool)
		if err != nil {
			log.Warn("failed to create volume", zap.Error(err))
			return "", 0, "", err
		}
		log.Debug("created volume name, size, pool")
		for _, initiator := range initiators {
			log := log.With(zap.String("initiator", initiator), zap.Int32("lun", lun))
			log.Debug("exporting volume")
			err = p.exportCreate(vol, lun, pool, initiator)
			if err != nil {
				log.Warn("failed to create export", zap.Error(err))
				return "", 0, "", err
			}
			log.Debug("exported volume")
			if getBool(options.StorageClass.Parameters["chapAuthSession"]) {
				log := log.With(zap.String("in_user", chapCredentials.InUser), zap.String("out_user", chapCredentials.OutUser))
				log.Debug("setting up chap session auth")
				err = p.setInitiatorAuth(initiator, chapCredentials.InUser, chapCredentials.InPassword, chapCredentials.OutUser, chapCredentials.OutPassword)
				if err != nil {
					log.Warn("failed to set up chap session auth", zap.Error(err))
					return "", 0, "", err
				}
				log.Debug("set up chap session auth")
			}
		}
	}
	return vol, lun, pool, nil
}

func getSize(options controller.ProvisionOptions) int64 {
	q := options.PVC.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)]
	return q.Value()
}

func (p *iscsiProvisioner) getVolumeName(options controller.ProvisionOptions) string {
	return options.PVName
}

func (p *iscsiProvisioner) getVolumeGroup(options controller.ProvisionOptions) string {
	if options.StorageClass.Parameters["volumeGroup"] == "" {
		return "vg-targetd"
	}
	return options.StorageClass.Parameters["volumeGroup"]
}

func (p *iscsiProvisioner) getInitiators(options controller.ProvisionOptions) []string {
	return strings.Split(options.StorageClass.Parameters["initiators"], ",")
}

// getFirstAvailableLun gets first available Lun.
func (p *iscsiProvisioner) getFirstAvailableLun(exportList exportList) (int32, error) {
	log := p.log
	sort.Sort(exportList)
	log.Debug("sorted export List: ", zap.Any("exportList", exportList))
	//this is sloppy way to remove duplicates
	uniqueExport := make(map[int32]export)
	for _, export := range exportList {
		uniqueExport[export.Lun] = export
	}
	log.Debug("unique luns sorted export List: ", zap.Any("exportList", uniqueExport))

	//this is a sloppy way to get the list of luns
	luns := make([]int, len(uniqueExport), len(uniqueExport))
	i := 0
	for _, export := range uniqueExport {
		luns[i] = int(export.Lun)
		i++
	}
	log.Debug("lun list", zap.Ints("luns",luns))

	if len(luns) >= 255 {
		return -1, errors.New("255 luns allocated no more luns available")
	}

	var sluns sort.IntSlice
	sluns = luns[0:]
	sort.Sort(sluns)
	log.Debug("sorted lun list", zap.Ints("luns",sluns))

	lun := int32(len(sluns))
	for i, clun := range sluns {
		if i < int(clun) {
			lun = int32(i)
			break
		}
	}
	return lun, nil
}

// volDestroy removes calls vol_destroy targetd API to remove volume.
func (p *iscsiProvisioner) volDestroy(vol string, pool string) error {
	log := p.log
	client, err := p.getConnection()
	defer client.Close()
	if err != nil {
		log.Warn("failed to get connection", zap.Error(err))
		return err
	}
	args := volDestroyArgs{
		Pool: pool,
		Name: vol,
	}
	err = client.Call("vol_destroy", args, nil)
	return err
}

// exportDestroy calls export_destroy targetd API to remove export of volume.
func (p *iscsiProvisioner) exportDestroy(vol string, pool string, initiator string) error {
	log := p.log
	client, err := p.getConnection()
	defer client.Close()
	if err != nil {
		log.Warn("failed to get connection", zap.Error(err))
		return err
	}
	args := exportDestroyArgs{
		Pool:         pool,
		Vol:          vol,
		InitiatorWwn: initiator,
	}
	err = client.Call("export_destroy", args, nil)
	return err
}

// volCreate calls vol_create targetd API to create a volume.
func (p *iscsiProvisioner) volCreate(name string, size int64, pool string) error {
	log := p.log
	client, err := p.getConnection()
	defer client.Close()
	if err != nil {
		log.Warn("failed to get connection", zap.Error(err))
		return err
	}
	args := volCreateArgs{
		Pool: pool,
		Name: name,
		Size: size,
	}
	err = client.Call("vol_create", args, nil)
	return err
}

// exportCreate calls export_create targetd API to create an export of volume.
func (p *iscsiProvisioner) exportCreate(vol string, lun int32, pool string, initiator string) error {
	log := p.log
	client, err := p.getConnection()
	defer client.Close()
	if err != nil {
		log.Warn("failed to get connection", zap.Error(err))
		return err
	}
	args := exportCreateArgs{
		Pool:         pool,
		Vol:          vol,
		InitiatorWwn: initiator,
		Lun:          lun,
	}
	err = client.Call("export_create", args, nil)
	return err
}

// exportList lists calls export_list targetd API to get export objects.
func (p *iscsiProvisioner) exportList() (exportList, error) {
	log := p.log
	client, err := p.getConnection()
	defer client.Close()
	if err != nil {
		log.Warn("failed to get connection", zap.Error(err))
		return nil, err
	}
	var result1 exportList
	err = client.Call("export_list", nil, &result1)
	return result1, err
}

//initiator_set_auth(initiator_wwn, in_user, in_pass, out_user, out_pass)

func (p *iscsiProvisioner) setInitiatorAuth(initiator string, inUser string, inPassword string, outUser string, outPassword string) error {
	log := p.log
	client, err := p.getConnection()
	defer client.Close()
	if err != nil {
		log.Warn("failed to get connection", zap.Error(err))
		return err
	}

	//make arguments object
	args := initiatorSetAuthArgs{
		InitiatorWwn: initiator,
		InUser:       inUser,
		InPassword:   inPassword,
		OutUser:      outUser,
		OutPassword:  outPassword,
	}
	//call remote procedure with args
	err = client.Call("initiator_set_auth", args, nil)
	return err
}

func (slice exportList) Len() int {
	return len(slice)
}

func (slice exportList) Less(i, j int) bool {
	return slice[i].Lun < slice[j].Lun
}

func (slice exportList) Swap(i, j int) {
	slice[i], slice[j] = slice[j], slice[i]
}

func (p *iscsiProvisioner) getConnection() (*jsonrpc2.Client, error) {
	log := p.log.With(zap.String("url",p.targetdURL))
	log.Debug("opening connection to targetd")

	client := jsonrpc2.NewHTTPClient(p.targetdURL)
	if client == nil {
		log.Warn("error creating the connection to targetd")
		return nil, errors.New("error creating the connection to targetd")
	}
	log.Debug("targetd client created")
	return client, nil
}

func (p *iscsiProvisioner) SupportsBlock() bool {
	return true
}
