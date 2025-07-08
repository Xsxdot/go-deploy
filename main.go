package main

import (
	"deploy/dns"
	"deploy/model"
	"deploy/nginx"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"gopkg.in/yaml.v3"
)

var (
	cfg       *model.Config
	workDir   = "./"
	backupDir = "./versions"
	version   = "dev" // 版本号，构建时通过ldflags注入
)

func main() {
	// 检查是否是查看版本信息
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("go-deploy %s\n", version)
		return
	}

	bytes, err := os.ReadFile(workDir + "conf/conf.yaml")
	if err != nil {
		panic(err)
	}
	cfg = new(model.Config)
	err = yaml.Unmarshal(bytes, cfg)
	if err != nil {
		panic(err)
	}

	serviceName := chooseService(err)
	version := ""
	service := new(model.Service)
	buildType := ""
	desc := ""

	operate := chooseOperate(serviceName)

	if operate == model.OperateTypeRollback {
		version = chooseRollback(serviceName)
	}

	serviceCfg, err := os.ReadFile(fmt.Sprintf("%sservices/%s.yaml", workDir, serviceName))
	if err != nil {
		panic(err)
	}
	err = yaml.Unmarshal(serviceCfg, service)
	if err != nil {
		panic(err)
	}

	env := chooseEnv(service, err)
	selectedServers := chooseServerWithSurvey(service, env)

	if operate == model.OperateTypeDeploy || operate == model.OperateTypeRollback {
		buildType = chooseFrontendAndBackend(service, version)
	}

	if operate == model.OperateTypeDeploy {
		version, desc = inputVersionAndDesc(serviceName)
	}

	// 根据操作类型执行相应的操作
	fmt.Printf("开始执行操作: %s\n", operate)
	switch operate {
	case model.OperateTypeDeploy:
		err = Deploy(service, env, version, desc, buildType, selectedServers)
		if err != nil {
			fmt.Printf("部署失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("部署完成!")

	case model.OperateTypeRollback:
		err = Rollback(service, env, version, buildType, selectedServers)
		if err != nil {
			fmt.Printf("回滚失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("回滚完成!")

	case model.OperateTypeStart:
		err = StartService(service, selectedServers)
		if err != nil {
			fmt.Printf("启动服务失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("服务启动完成!")

	case model.OperateTypeStop:
		err = StopService(service, selectedServers)
		if err != nil {
			fmt.Printf("停止服务失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("服务停止完成!")

	case model.OperateTypeRestart:
		err = RestartService(service, selectedServers)
		if err != nil {
			fmt.Printf("重启服务失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("服务重启完成!")

	case model.OperateTypeCreateNginx:
		err = nginx.CreateNginxConf(service, env, selectedServers, cfg.NginxServers, cfg.NginxConfDir)
		if err != nil {
			fmt.Printf("创建nginx配置失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Nginx配置创建完成!")

	case model.OperateTypeCreateDNS:
		err = dns.CreateOrUpdateDNSRecord(service, env, selectedServers, cfg.DNSKey, cfg.DNSSecret)
		if err != nil {
			fmt.Printf("创建DNS记录失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("DNS记录创建完成!")

	case model.OperateTypeUninstall:
		err = Uninstall(service, env, selectedServers)
		if err != nil {
			fmt.Printf("卸载失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("卸载完成!")

	default:
		fmt.Printf("未知的操作类型: %s\n", operate)
		os.Exit(1)
	}
}

func inputVersionAndDesc(serviceName string) (string, string) {
	var version string
	versionPrompt := &survey.Input{
		Message: "请输入版本号:",
		Help:    "例如: 1.0.0, 2.1.3",
	}
	err := survey.AskOne(versionPrompt, &version)
	if err != nil {
		panic(err)
	}

	dir, err := os.ReadDir(filepath.Join(backupDir, serviceName))
	if err != nil {
		panic(err)
	}
	for _, entry := range dir {
		if strings.HasPrefix(entry.Name(), version) {
			fmt.Printf("版本号已存在: %s\n", version)
			return inputVersionAndDesc(serviceName)
		}
	}

	var desc string
	descPrompt := &survey.Input{
		Message: "请输入部署描述:",
		Help:    "简要描述本次部署的内容",
	}
	err = survey.AskOne(descPrompt, &desc)
	if err != nil {
		panic(err)
	}

	return version, desc
}

func chooseRollback(name string) string {
	dir, err := os.ReadDir(backupDir + "/" + name)
	if err != nil {
		panic(err)
	}

	var versions []string
	for _, entry := range dir {
		if !entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			versions = append(versions, entry.Name())
		}
	}

	sort.Slice(versions, func(i, j int) bool {
		return versions[i] < versions[j]
	})

	prompt := &survey.Select{
		Message: "请选择操作:",
		Options: versions,
		Default: versions[0],
	}

	var version string
	err = survey.AskOne(prompt, &version)
	if err != nil {
		panic(err)
	}

	return strings.Split(version, "-")[0]
}

func chooseServerWithSurvey(service *model.Service, env model.Env) []string {
	var hosts []string
	if service.TestEnv != nil {
		hosts = service.TestEnv.Servers
	} else if service.ProdEnv != nil {
		hosts = service.ProdEnv.Servers
	}

	if len(hosts) == 0 {
		panic("没有找到对应环境的服务器")
	}

	var selectedHosts []string
	prompt := &survey.MultiSelect{
		Message:  "请选择服务器 (使用空格键选择，回车键确认):",
		Options:  hosts,
		PageSize: 10,
	}

	err := survey.AskOne(prompt, &selectedHosts)
	if err != nil {
		panic(err)
	}

	if len(selectedHosts) == 0 {
		panic("必须至少选择一个服务器")
	}

	return selectedHosts
}

func chooseFrontendAndBackend(service *model.Service, version string) string {
	var ops []string
	if version == "" {
		if service.FrontendWorkDir != "" {
			ops = append(ops, "前端")
		}
		if service.BackendWorkDir != "" {
			ops = append(ops, "后端")
		}
	} else {
		dir, err := os.ReadDir(backupDir + "/" + service.ServiceName)
		if err != nil {
			panic(err)
		}
		for _, d := range dir {
			if !d.IsDir() && strings.HasPrefix(d.Name(), version) {
				//1.0.0-test-all-desc.tar.gz
				split := strings.Split(d.Name(), "-")
				if len(split) >= 4 {
					if split[2] == "front" {
						ops = append(ops, "前端")
					} else if split[2] == "back" {
						ops = append(ops, "后端")
					} else {
						ops = append(ops, "前端", "后端")
					}
				} else {
					panic("错误的版本号")
				}
			}
		}
	}
	var selectedOperation string

	if len(ops) == 0 {
		panic("没有指定代码目录")
	} else {
		options := []string{"前后端", "前端", "后端"}
		if len(ops) == 1 {
			options = ops
		}
		prompt := &survey.Select{
			Message: "请选择类型:",
			Options: options,
			Default: options[0],
		}

		err := survey.AskOne(prompt, &selectedOperation)
		if err != nil {
			panic(err)
		}
	}

	switch selectedOperation {
	case "前后端":
		return "all"
	case "前端":
		return "front"
	case "后端":
		return "back"
	}
	panic("无效的操作")
}

func chooseOperate(serviceName string) model.OperateType {
	servicePath := backupDir + "/" + serviceName
	err := os.MkdirAll(servicePath, os.ModePerm)
	if err != nil {
		panic(err)
	}

	dir, err := os.ReadDir(servicePath)
	if err != nil {
		panic(err)
	}

	var operates []string
	var operateTypes []model.OperateType

	if len(dir) == 0 {
		operates = []string{"部署", "启动", "停止", "重启", "卸载", "重建Nginx", "重建DNS"}
		operateTypes = []model.OperateType{model.OperateTypeDeploy, model.OperateTypeStart, model.OperateTypeStop, model.OperateTypeRestart, model.OperateTypeUninstall, model.OperateTypeCreateNginx, model.OperateTypeCreateDNS}
	} else {
		operates = []string{"部署", "回滚", "启动", "停止", "重启", "卸载", "重建Nginx", "重建DNS"}
		operateTypes = []model.OperateType{model.OperateTypeDeploy, model.OperateTypeRollback, model.OperateTypeStart, model.OperateTypeStop, model.OperateTypeRestart, model.OperateTypeUninstall, model.OperateTypeCreateNginx, model.OperateTypeCreateDNS}
	}

	var selectedOperation string
	prompt := &survey.Select{
		Message: "请选择操作:",
		Options: operates,
		Default: operates[0],
	}

	err = survey.AskOne(prompt, &selectedOperation)
	if err != nil {
		panic(err)
	}

	// 找到选择的操作对应的索引
	var selectedIndex int
	for i, op := range operates {
		if op == selectedOperation {
			selectedIndex = i
			break
		}
	}

	return operateTypes[selectedIndex]
}

func chooseEnv(service *model.Service, err error) model.Env {
	var deployTypeOps []string
	var envs []model.Env
	if service.TestEnv != nil {
		deployTypeOps = append(deployTypeOps, "测试环境")
		envs = append(envs, model.Test)
	}
	if service.ProdEnv != nil {
		deployTypeOps = append(deployTypeOps, "生产环境")
		envs = append(envs, model.Prod)
	}
	if len(deployTypeOps) == 0 {
		panic("没有配置环境")
	}

	var selectedEnv string
	prompt := &survey.Select{
		Message: "请选择环境:",
		Options: deployTypeOps,
		Default: deployTypeOps[0],
	}

	err = survey.AskOne(prompt, &selectedEnv)
	if err != nil {
		panic(err)
	}

	// 找到选择的环境对应的索引
	var selectedIndex int
	for i, env := range deployTypeOps {
		if env == selectedEnv {
			selectedIndex = i
			break
		}
	}

	return envs[selectedIndex]
}

func chooseService(err error) string {
	dir, err := os.ReadDir(workDir + "services")
	if err != nil {
		panic(err)
	}
	services := make([]string, 0)
	for _, entry := range dir {
		if !entry.IsDir() {
			services = append(services, strings.Split(entry.Name(), ".")[0])
		}
	}

	if len(services) == 0 {
		panic("没有找到任何服务配置文件")
	}

	var selectedService string
	prompt := &survey.Select{
		Message:  "请选择一个服务:",
		Options:  services,
		PageSize: 10,
	}

	err = survey.AskOne(prompt, &selectedService)
	if err != nil {
		log.Fatal("选择服务失败：", err)
	}

	return selectedService
}
