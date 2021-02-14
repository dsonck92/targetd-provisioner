/*
Copyright 2016 The Kubernetes Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"context"
	"fmt"
	"go.sonck.nl/targetd-provisioner/iscsi"
	"go.sonck.nl/targetd-provisioner/nfs"
	"go.uber.org/zap"
	"os"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v6/controller"
	"sync"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// start-controllerCmd represents the start-controller command
var startcontrollerCmd = &cobra.Command{
	Use:   "start",
	Short: "Start a targetd dynamic provisioner",
	Long:  `Start a targetd dynamic provisioner`,
	Run: func(cmd *cobra.Command, args []string) {
		log, err := zap.NewProduction()
		if err != nil {
			_,_ = fmt.Fprintf(os.Stderr, "Failed to initialize logging: %v", err.Error())
			os.Exit(1)
		}
		log.Debug("start called")
		var config *rest.Config
		master := viper.GetString("master")
		kubeconfig := viper.GetString("kubeconfig")
		// creates the in-cluster config
		log.Debug("creating in cluster default kube client config")
		if master != "" || kubeconfig != "" {
			config, err = clientcmd.BuildConfigFromFlags(master, kubeconfig)
		} else {
			config, err = rest.InClusterConfig()
		}
		if err != nil {
			log.Fatal("failed to get cluster config", zap.Error(err))
		}
		log.Debug("kube client config created", zap.String(
			"config-host", config.Host))

		// creates the clientset
		log.Debug("creating kube client set")
		kubernetesClientSet, err := kubernetes.NewForConfig(config)
		if err != nil {
			log.Fatal("Failed to create kube client set", zap.Error(err))
		}
		log.Debug("kube client set created")

		// The controller needs to know what the server version is because out-of-tree
		// provisioners aren't officially supported until 1.5
		serverVersion, err := kubernetesClientSet.Discovery().ServerVersion()
		if err != nil {
			log.Fatal("Error getting server version", zap.Error(err))
		}

		url := fmt.Sprintf("%s://%s:%s@%s:%d/targetrpc", viper.GetString("targetd-scheme"), viper.GetString("targetd-username"), viper.GetString("targetd-password"), viper.GetString("targetd-address"), viper.GetInt("targetd-port"))

		log.Debug("targetd URL", zap.String("url", url))

		iscsiProvisioner := iscsi.NewiscsiProvisioner(url, log)
		log.Debug("iscsi provisioner created")

		var wg sync.WaitGroup

		iscsiPc := controller.NewProvisionController(kubernetesClientSet, viper.GetString("iscsi-provisioner-name"), iscsiProvisioner, serverVersion.GitVersion, controller.Threadiness(1),
			controller.ResyncPeriod(viper.GetDuration("resync-period")),
			controller.ExponentialBackOffOnError(viper.GetBool("exponential-backoff-on-error")),
			controller.FailedProvisionThreshold(viper.GetInt("fail-retry-threshold")),
			controller.FailedDeleteThreshold(viper.GetInt("fail-retry-threshold")),
			controller.LeaseDuration(viper.GetDuration("lease-period")),
			controller.RenewDeadline(viper.GetDuration("renew-deadline")),
			controller.RetryPeriod(viper.GetDuration("retry-period")))
		log.Debug("iscsi controller created, running forever...")
		wg.Add(1)
		go func() {
		iscsiPc.Run(context.Background())
			wg.Done()
		}()

		nfsProvisioner := nfs.NewnfsProvisioner(url, log)
		log.Debug("iscsi provisioner created")

		nfsPc := controller.NewProvisionController(kubernetesClientSet, viper.GetString("nfs-provisioner-name"), nfsProvisioner, serverVersion.GitVersion, controller.Threadiness(1),
			controller.ResyncPeriod(viper.GetDuration("resync-period")),
			controller.ExponentialBackOffOnError(viper.GetBool("exponential-backoff-on-error")),
			controller.FailedProvisionThreshold(viper.GetInt("fail-retry-threshold")),
			controller.FailedDeleteThreshold(viper.GetInt("fail-retry-threshold")),
			controller.LeaseDuration(viper.GetDuration("lease-period")),
			controller.RenewDeadline(viper.GetDuration("renew-deadline")),
			controller.RetryPeriod(viper.GetDuration("retry-period")))
		log.Debug("iscsi controller created, running forever...")
		wg.Add(1)
		go func() {
		nfsPc.Run(context.Background())
			wg.Done()
		}()

		wg.Wait()
	},
}

func init() {
	RootCmd.AddCommand(startcontrollerCmd)
	startcontrollerCmd.Flags().String("iscsi-provisioner-name", "iscsi-targetd", "name of this provisioner, must match what is passed in the storage class annotation")
	viper.BindPFlag("iscsi-provisioner-name", startcontrollerCmd.Flags().Lookup("iscsi-provisioner-name"))
	startcontrollerCmd.Flags().String("nfs-provisioner-name", "nfs-targetd", "name of this provisioner, must match what is passed in the storage class annotation")
	viper.BindPFlag("nfs-provisioner-name", startcontrollerCmd.Flags().Lookup("nfs-provisioner-name"))
	startcontrollerCmd.Flags().Duration("resync-period", controller.DefaultResyncPeriod, "how often to poll the master API for updates")
	viper.BindPFlag("resync-period", startcontrollerCmd.Flags().Lookup("resync-period"))
	startcontrollerCmd.Flags().Bool("exponential-backoff-on-error", controller.DefaultExponentialBackOffOnError, "exponential-backoff-on-error doubles the retry-period everytime there is an error")
	viper.BindPFlag("exponential-backoff-on-error", startcontrollerCmd.Flags().Lookup("exponential-backoff-on-error"))
	startcontrollerCmd.Flags().Int("fail-retry-threshold", controller.DefaultFailedProvisionThreshold, "Threshold for max number of retries on failure of provisioner")
	viper.BindPFlag("fail-retry-threshold", startcontrollerCmd.Flags().Lookup("fail-retry-threshold"))
	startcontrollerCmd.Flags().Duration("lease-period", controller.DefaultLeaseDuration, "LeaseDuration is the duration that non-leader candidates will wait to force acquire leadership. This is measured against time of last observed ack")
	viper.BindPFlag("lease-period", startcontrollerCmd.Flags().Lookup("lease-period"))
	startcontrollerCmd.Flags().Duration("renew-deadline", controller.DefaultRenewDeadline, "RenewDeadline is the duration that the acting master will retry refreshing leadership before giving up")
	viper.BindPFlag("renew-deadline", startcontrollerCmd.Flags().Lookup("renew-deadline"))
	startcontrollerCmd.Flags().Duration("retry-period", controller.DefaultRetryPeriod, "RetryPeriod is the duration the LeaderElector clients should wait between tries of actions")
	viper.BindPFlag("retry-period", startcontrollerCmd.Flags().Lookup("retry-period"))
	startcontrollerCmd.Flags().String("targetd-scheme", "http", "scheme of the targetd connection, can be http or https")
	viper.BindPFlag("targetd-scheme", startcontrollerCmd.Flags().Lookup("targetd-scheme"))
	startcontrollerCmd.Flags().String("targetd-username", "admin", "username for the targetd connection")
	viper.BindPFlag("targetd-username", startcontrollerCmd.Flags().Lookup("targetd-username"))
	startcontrollerCmd.Flags().String("targetd-password", "", "password for the targetd connection")
	viper.BindPFlag("targetd-password", startcontrollerCmd.Flags().Lookup("targetd-password"))
	startcontrollerCmd.Flags().String("targetd-address", "localhost", "ip or dns of the targetd server")
	viper.BindPFlag("targetd-address", startcontrollerCmd.Flags().Lookup("targetd-address"))
	startcontrollerCmd.Flags().Int("targetd-port", 18700, "port on which targetd is listening")
	viper.BindPFlag("targetd-port", startcontrollerCmd.Flags().Lookup("targetd-port"))
	startcontrollerCmd.Flags().String("default-fs", "xfs", "filesystem to use when not specified")
	viper.BindPFlag("default-fs", startcontrollerCmd.Flags().Lookup("default-fs"))
	startcontrollerCmd.Flags().String("master", "", "Master URL")
	viper.BindPFlag("master", startcontrollerCmd.Flags().Lookup("master"))
	startcontrollerCmd.Flags().String("kubeconfig", "", "Absolute path to the kubeconfig")
	viper.BindPFlag("kubeconfig", startcontrollerCmd.Flags().Lookup("kubeconfig"))
	startcontrollerCmd.Flags().String("session-chap-credential-file-path", "/var/run/secrets/iscsi-provisioner/session-chap-credential.properties", "path where the credential for session chap authentication can be found")
	viper.BindPFlag("session-chap-credential-file-path", startcontrollerCmd.Flags().Lookup("session-chap-credential-file-path"))

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// start-controllerCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// start-controllerCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")

}
