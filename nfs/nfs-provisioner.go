package nfs

import (
	"context"
	"errors"
	"fmt"
	"github.com/powerman/rpc-codec/jsonrpc2"
	"go.sonck.nl/targetd-provisioner/targetd"
	"go.uber.org/zap"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v6/controller"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v6/util"
	"strconv"
	"strings"
)

type volCreateArgs struct {
	Pool string `json:"pool_name"`
	Name string `json:"name"`
	Size int64  `json:"size_bytes"`
}

type volDestroyArgs struct {
	Uuid string `json:"uuid"`
}

type exportCreateArgs struct {
	Host    string   `json:"host"`
	Path    string   `json:"path"`
	Options []string `json:"options"`
}

type exportDestroyArgs struct {
	Host string `json:"host"`
	Path string `json:"path"`
}

type volume struct {
	Name       string `json:"name"`
	Uuid       string `json:"uuid"`
	TotalSpace int64  `json:"total_space"`
	FreeSpace  int64  `json:"free_space"`
	Pool       string `json:"pool"`
	FullPath   string `json:"full_path"`
}

type volumeList []volume

type nfsProvisioner struct {
	targetdURL string
	log        *zap.Logger
}

type export struct {
	Host    string   `json:"host"`
	Path    string   `json:"path"`
	Options []string `json:"options"`
}

type exportList []export

func NewnfsProvisioner(url string, logger *zap.Logger) controller.Provisioner {
	return &nfsProvisioner{
		targetdURL: url,
		log:        logger.With(zap.String("system","nfs")),
	}
}

func (p *nfsProvisioner) getAccessModes() []v1.PersistentVolumeAccessMode {
	return []v1.PersistentVolumeAccessMode{
		v1.ReadWriteMany,
		v1.ReadOnlyMany,
		v1.ReadWriteOnce,
	}
}

func (p *nfsProvisioner) Provision(context context.Context, options controller.ProvisionOptions) (*v1.PersistentVolume, controller.ProvisioningState, error) {
	if !util.AccessModesContainedInAll(p.getAccessModes(), options.PVC.Spec.AccessModes) {
		return nil, controller.ProvisioningNoChange, fmt.Errorf("invalid AccessModes %v: only AccessModes %v are supported", options.PVC.Spec.AccessModes, p.getAccessModes())
	}
	p.log.Debug("new provision request received for pvc", zap.String("name", options.PVName))
	vol, _, path, uuid, err := p.createVolume(options)
	if err != nil {
		p.log.Warn("failed to create volume", zap.Error(err))
		return nil, controller.ProvisioningNoChange, err
	}
	p.log.Debug("volume created", zap.String("volume", vol), zap.String("path", path))

	annotations := make(map[string]string)
	annotations["uuid"] = uuid
	annotations["hosts"] = options.StorageClass.Parameters["hosts"]

	host := options.StorageClass.Parameters["host"]

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
				v1.ResourceStorage: options.PVC.Spec.Resources.Requests[v1.ResourceStorage],
			},
			VolumeMode: options.PVC.Spec.VolumeMode,
			PersistentVolumeSource: v1.PersistentVolumeSource{
				NFS: &v1.NFSVolumeSource{
					Server:   host,
					Path:     path,
					ReadOnly: getReadOnly(options.StorageClass.Parameters["readonly"]),
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

func (p *nfsProvisioner) Delete(context context.Context, volume *v1.PersistentVolume) error {
	log := p.log.With(zap.String("vol", volume.GetName()), zap.String("uuid", volume.Annotations["uuid"]))
	log.Debug("volume deletion request")
	for _, host := range strings.Split(volume.Annotations["hosts"], ",") {
		log := log.With(zap.String("host", host), zap.String("path", volume.Spec.NFS.Path))
		log.Debug("removing nfs export")
		err := p.exportDestroy(host, volume.Spec.NFS.Path)
		if err != nil {
			var errorInfo targetd.ErrorInfo
			err2 := json.Unmarshal([]byte(err.Error()), &errorInfo)
			if err2 != nil {
				return err
			}

			if errorInfo.Code != targetd.NotFoundNfsExport {
				log.Warn("failed to destroy nfs export", zap.Error(errorInfo))
				return errorInfo
			} else {
				log.Warn("nfs export was already removed")
			}
		}
		log.Debug("nfs export removed")
	}
	log.Debug("removing filesystem volume")
	err := p.volDestroy(volume.Annotations["uuid"])
	if err != nil {
		var errorInfo targetd.ErrorInfo
		err2 := json.Unmarshal([]byte(err.Error()), &errorInfo)
		if err2 != nil {
			log.Warn("failed to destroy filesystem volume", zap.Error(err))
		}
		if errorInfo.Code != targetd.NotFoundVolume {
			log.Warn("failed to destroy filesystem volume", zap.Error(errorInfo))
		}
	}
	log.Debug("logical volume removed")
	log.Debug("volume deletion request completed")
	return nil
}

func (p *nfsProvisioner) createVolume(options controller.ProvisionOptions) (vol, pool, path, uuid string, err error) {
	vol = p.getVolumeName(options)
	pool = p.getVolumeGroup(options)
	hosts := p.getHosts(options)
	nfsOpts := p.getNfsOptions(options)

	p.log.Debug("creating volume", zap.String("name", vol), zap.String("pool", pool))
	err = p.volCreate(vol, pool)
	if err != nil {
		p.log.Warn("failed to create volume", zap.Error(err))
		return "", "", "", "", err
	}

	path, uuid, err = p.volFind(vol, pool)
	if err != nil {
		p.log.Warn("failed to find created volume", zap.Error(err))
		return "", "", "", "", err
	}
	p.log.Debug("created volume", zap.String("name", vol), zap.String("pool", pool), zap.String("fullPath", path))

	for _, host := range hosts {
		p.log.Debug("exporting volume", zap.String("name", vol), zap.String("pool", pool), zap.String("host", host))
		err = p.exportCreate(path, host, nfsOpts)
		if err != nil {
			p.log.Warn("failed to create export", zap.Error(err))
		}
	}
	return
}

func (p *nfsProvisioner) getVolumeName(options controller.ProvisionOptions) string {
	return options.PVName
}

func (p *nfsProvisioner) getVolumeGroup(options controller.ProvisionOptions) string {
	if options.StorageClass.Parameters["volumeGroup"] == "" {
		return "vg-targetd"
	}
	return options.StorageClass.Parameters["volumeGroup"]
}

func (p *nfsProvisioner) getHosts(options controller.ProvisionOptions) []string {
	return strings.Split(options.StorageClass.Parameters["hosts"], ",")
}

func (p *nfsProvisioner) getNfsOptions(options controller.ProvisionOptions) []string {
	return strings.Split(options.StorageClass.Parameters["options"], ",")
}

func (p *nfsProvisioner) volCreate(name, pool string) error {
	client, err := p.getConnection()
	defer client.Close()
	if err != nil {
		p.log.Warn("failed to get connection", zap.Error(err))
		return err
	}
	args := volCreateArgs{
		Pool: pool,
		Name: name,
		Size: 0,
	}
	err = client.Call("fs_create", args, nil)
	return err
}

func (p *nfsProvisioner) volFind(name, pool string) (path, uuid string, err error) {
	volumeList, err := p.volList()
	if err != nil {
		p.log.Warn("failed to get volumes", zap.Error(err))
	}
	for _, volume := range volumeList {
		if volume.Pool == pool && volume.Name == name {
			path = volume.FullPath
			uuid = volume.Uuid
			return
		}
	}
	return "", "", errors.New("failed to find the created volume")
}

func (p *nfsProvisioner) volDestroy(uuid string) error {
	client, err := p.getConnection()
	defer client.Close()
	if err != nil {
		p.log.Warn("failed to get connection", zap.Error(err))
	}
	args := volDestroyArgs{
		Uuid: uuid,
	}
	err = client.Call("fs_destroy", args, nil)
	return err
}

func (p *nfsProvisioner) exportCreate(fullPath, host string, nfsOptions []string) error {
	client, err := p.getConnection()
	defer client.Close()
	if err != nil {
		p.log.Warn("failed to get connection", zap.Error(err))
		return err
	}
	args := exportCreateArgs{
		Host:    host,
		Path:    fullPath,
		Options: nfsOptions,
	}
	err = client.Call("nfs_export_add", args, nil)
	return err
}

func (p *nfsProvisioner) exportDestroy(fullPath, host string) error {
	client, err := p.getConnection()
	defer client.Close()
	if err != nil {
		p.log.Warn("failed to get connection", zap.Error(err))
		return err
	}
	args := exportDestroyArgs{
		Host: host,
		Path: fullPath,
	}
	err = client.Call("nfs_export_remove", args, nil)
	return err
}

func (p *nfsProvisioner) volList() (volumeList, error) {
	client, err := p.getConnection()
	defer client.Close()
	if err != nil {
		p.log.Warn("failed to get connection", zap.Error(err))
		return nil, err
	}
	var result1 volumeList
	err = client.Call("fs_list", nil, &result1)
	if err != nil {
		p.log.Warn("failed to get fs_list", zap.Error(err))
		return nil, err
	}
	return result1, err
}

func (p *nfsProvisioner) exportList() (exportList, error) {
	client, err := p.getConnection()
	defer client.Close()
	if err != nil {
		p.log.Warn("failed to get connection", zap.Error(err))
		return nil, err
	}
	var result1 exportList
	err = client.Call("nfs_export_list", nil, &result1)
	if err != nil {
		p.log.Warn("failed to get nfs_export_list", zap.Error(err))
		return nil, err
	}
	return result1, nil
}

func (p *nfsProvisioner) getConnection() (*jsonrpc2.Client, error) {
	log := p.log.With(zap.String("url", p.targetdURL))
	log.Debug("opening connection to targetd")

	client := jsonrpc2.NewHTTPClient(p.targetdURL)
	if client == nil {
		p.log.Warn("error creating the connection to targetd", zap.String("url", p.targetdURL))
		return nil, errors.New("error creating the connection to targetd")
	}
	log.Debug("targetd client created")
	return client, nil
}

func (p *nfsProvisioner) SupportsBlock() bool {
	return false
}
