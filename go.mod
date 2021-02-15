module go.sonck.nl/targetd-provisioner

go 1.14

require (
	github.com/magiconair/properties v1.8.1
	github.com/powerman/rpc-codec v1.2.2
	github.com/spf13/cobra v1.0.0
	github.com/spf13/viper v1.7.0
	go.uber.org/zap v1.15.0
	k8s.io/api v0.18.6
	k8s.io/apimachinery v0.18.6
	k8s.io/client-go v0.18.6
	k8s.io/utils v0.0.0-20200731180307-f00132d28269 // indirect; indirect v6
	sigs.k8s.io/sig-storage-lib-external-provisioner/v6 v6.0.0
)
