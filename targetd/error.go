package targetd

type ErrorCode int

const (
	// Common
	Invalid            ErrorCode = -1
	NameConflict       ErrorCode = -50
	NoSupport          ErrorCode = -153
	UnexpectedExitCode ErrorCode = -303
	InvalidArgument    ErrorCode = -32602

	// Specific to block
	ExistsInitiator     ErrorCode = -52
	NotFoundVolume      ErrorCode = -103
	NotFoundVolumeGroup ErrorCode = -152
	NotFoundAccessGroup ErrorCode = -200
	VolumeMasked        ErrorCode = -303
	NoFreeHostLunId     ErrorCode = -1000

	// Specific to FS/NFS
	ExistsCloneName      ErrorCode = -51
	ExistsFsName         ErrorCode = -53
	NotFoundFs           ErrorCode = -104
	InvalidPool          ErrorCode = -110
	NotFoundSs           ErrorCode = -112
	NotFoundVolumeExport ErrorCode = -151
	NotFoundNfsExport    ErrorCode = -400
	NfsNoSupport         ErrorCode = -401
)

type ErrorInfo struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func (i ErrorInfo) Error() string {
	return i.Message
}
