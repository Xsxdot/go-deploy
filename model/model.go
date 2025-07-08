package model

type Service struct {
	ServiceName     string     `json:"serviceName" yaml:"serviceName"`
	SSL             bool       `json:"ssl" yaml:"ssl"`
	SSLKeyPath      string     `json:"sslKeyPath" yaml:"sslKeyPath"`
	SSLCertPath     string     `json:"sslCertPath" yaml:"sslCertPath"`
	StartCommand    string     `json:"startCommand" yaml:"startCommand"`
	TestEnv         *EnvConfig `json:"testEnv" yaml:"testEnv"`
	ProdEnv         *EnvConfig `json:"prodEnv" yaml:"prodEnv"`
	FrontendWorkDir string     `json:"frontendWorkDir" yaml:"frontendWorkDir"`
	BackendWorkDir  string     `json:"backendWorkDir" yaml:"backendWorkDir"`
	CopyFiles       []CopyFile `json:"copyFiles" yaml:"copyFiles"`
}

type CopyFile struct {
	Source string   `json:"source" yaml:"source"`
	Target string   `json:"target" yaml:"target"`
	Mode   Mode     `json:"mode" yaml:"mode"`
	Type   FileType `json:"type" yaml:"type"`
}

type FileType string

const (
	FileTypeFile FileType = "front"
	FileTypeDir  FileType = "back"
)

type EnvConfig struct {
	Domain                string   `json:"domain" yaml:"domain"`
	InstallPath           string   `json:"installPath" yaml:"installPath"`
	Port                  int      `json:"port" yaml:"port"`
	FrontendBuildCommands []string `json:"frontendBuildCommands" yaml:"frontendBuildCommands"`
	BackendBuildCommands  []string `json:"backendBuildCommands" yaml:"backendBuildCommands"`
	HealthUrl             string   `json:"healthUrl" yaml:"healthUrl"`
	Servers               []string `json:"servers" yaml:"servers"`
}

type OperateType string

const (
	OperateTypeDeploy      OperateType = "deploy"
	OperateTypeRollback    OperateType = "rollback"
	OperateTypeStart       OperateType = "start"
	OperateTypeStop        OperateType = "stop"
	OperateTypeRestart     OperateType = "restart"
	OperateTypeUninstall   OperateType = "uninstall"
	OperateTypeCreateNginx OperateType = "create_nginx"
	OperateTypeCreateDNS   OperateType = "create_dns"
)

type Env string

const (
	Test Env = "test"
	Prod Env = "prod"
)

type Mode string

const (
	ModeMove  Mode = "move"
	ModeCopy  Mode = "copy"
	ModeMkdir Mode = "mkdir"
)

type Config struct {
	NginxServers []string `json:"nginxServers" yaml:"nginxServers"`
	NginxConfDir string   `json:"nginxConfDir" yaml:"nginxConfDir"`
	DNSKey       string   `json:"dnsKey" yaml:"dnsKey"`
	DNSSecret    string   `json:"dnsSecret" yaml:"dnsSecret"`
}

type Server struct {
	Host string `json:"host" yaml:"host"`
	Env  Env    `json:"env" yaml:"env"`
}
