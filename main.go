// K8S TSNCNI PLUGIN Andrea Barigazzi
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/containernetworking/plugins/pkg/ipam"
	bv "github.com/containernetworking/plugins/pkg/utils/buildversion"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Declare logger
var logger, _ = setupLogger("/var/log/tsncni-plugin.log")

// Config logger
func setupLogger(logFilePath string) (*log.Logger, error) {
	file, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	logger := log.New(file, "CNI-DEBUG: ", log.Ldate|log.Ltime|log.Lshortfile)
	return logger, nil
}

// PluginConf  NetConf + RuntimeCustomConfs
type PluginConf struct {
	types.NetConf
	RuntimeConfig *struct {
		SampleConfig map[string]interface{} `json:"sample"`
	} `json:"runtimeConfig"`
	// Plugin-specific flags
	MyAwesomeFlag     bool   `json:"myAwesomeFlag"`
	AnotherAwesomeArg string `json:"anotherAwesomeArg"`
}

func parseConfig(stdin []byte) (*PluginConf, error) {
	conf := PluginConf{}
	if err := json.Unmarshal(stdin, &conf); err != nil {
		return nil, fmt.Errorf("failed to parse network configuration: %v", err)
	}
	if err := version.ParsePrevResult(&conf.NetConf); err != nil {
		return nil, fmt.Errorf("could not parse prevResult: %v", err)
	}
	if conf.AnotherAwesomeArg == "" {
		return nil, fmt.Errorf("anotherAwesomeArg must be specified")
	}
	return &conf, nil
}

// cmdAdd for ADD requests
func cmdAdd(args *skel.CmdArgs) error {
	conf, err := parseConfig(args.StdinData)
	if err != nil {
		return err
	}
	// START ADD

	//CALL IPAM PLUGIN==================================================================================================
	configString := arrbyteToString(args.StdinData) //StdinData è un array di byte...
	ipamOutput, err := callIpamPlugin(configString, args.ContainerID, args.Netns, args.IfName, "ADD")
	var ipamOutputJson map[string]interface{}
	ipamOutputStr, err := json.MarshalIndent(ipamOutputJson, "", "  ")
	if err != nil {
		logger.Printf("Errore nella serializzazione del JSON: %v", err)
		return err
	}
	logger.Printf("IPAM HOST-LOCAL OUTPUT JSON:\n%s", string(ipamOutputStr))

	//endCALL IPAM =====================================================================================================

	// CHECK IF CHAINED PLUGIN
	if conf.PrevResult != nil {
		return fmt.Errorf("must be called as the first plugin")
	}
	//==================================================================================================================
	//==================================================================================================================
	// Check OVS status, create if needed...
	//assert_or_create_ovs_bridge()    TODO
	//==================================================================================================================
	// get ip, cidr and gw from ipam output
	err = json.Unmarshal([]byte(ipamOutput), &ipamOutputJson)
	if err != nil {
		return err
	}
	ip, cidr, gw, _ := getIpamNetData(ipamOutputJson)
	cidrInt, _ := strconv.Atoi(cidr)
	bridge := "br-int"
	ofPortNum := "1" //
	podNetNamespace := args.Netns

	logger.Printf("Ipam plugin result: %v %v %v", ip, cidr, gw)
	logger.Printf("pod_net_namespace: %v", podNetNamespace)
	logger.Printf("All args: %v", args)

	ovsPort, containerPort := createVethPair(args.ContainerID)

	logger.Printf("ovs_port: %v", ovsPort)
	logger.Printf("container_port: %v", containerPort)

	setOvsPort(bridge, ovsPort, ofPortNum)
	setPodPort(containerPort, podNetNamespace, ip, cidr, gw)

	// add to the result
	result := &current.Result{CNIVersion: current.ImplementedSpecVersion}
	result.Interfaces = []*current.Interface{
		{
			Name:    containerPort,
			Sandbox: args.Netns,
			Mac:     "00:11:22:33:44:55",
		},
	}
	result.IPs = []*current.IPConfig{
		{
			Address: net.IPNet{
				IP:   net.ParseIP(ip),
				Mask: net.CIDRMask(cidrInt, 32),
			},
			Gateway:   net.ParseIP(gw),
			Interface: current.Int(0),
		},
	}
	logger.Printf("Result: %v", result)
	// Pass through the result for the next plugin
	return types.PrintResult(result, conf.CNIVersion)
}

// cmdDel DELETE requests
func cmdDel(args *skel.CmdArgs) error {
	conf, err := parseConfig(args.StdinData)
	if err != nil {
		return err
	}
	_ = conf
	//Delete veth from OVS bridge=======================================================================================
	bridge := "br-int"
	containerId := args.ContainerID
	id := containerId[len(containerId)-8:]
	ovsPort := "veth" + id
	// delete veth port in OVS bridge
	cmd := exec.Command("ovs-vsctl", "del-port", bridge, ovsPort)
	err = cmd.Run()
	if err != nil {
		return err
	}
	logger.Printf("Deleted port %v from ovs bridge %v.", ovsPort, bridge)
	//==================================================================================================================
	//libera IP assegnato dal plugin IPAM
	configString := arrbyteToString(args.StdinData)
	_, err = callIpamPlugin(configString, args.ContainerID, args.Netns, args.IfName, "DEL")
	logger.Printf("IPAM HOST-LOCAL: IP DELETED") // se nil tutto ok
	return nil
}

func main() {
	bv.BuildVersion = "1.0.1-beta"
	skel.PluginMainFuncs(skel.CNIFuncs{
		Add:    cmdAdd,
		Check:  cmdCheck,
		Del:    cmdDel,
		Status: cmdStatus,
	}, version.All, bv.BuildString("TSNCNI"))

}

func cmdCheck(_ *skel.CmdArgs) error {
	// TODO: implement
	return fmt.Errorf("not implemented")
}

func cmdStatus(args *skel.CmdArgs) error {
	conf, err := parseConfig(args.StdinData)
	if err != nil {
		return err
	}
	_ = conf
	if err := ipam.ExecStatus(conf.IPAM.Type, args.StdinData); err != nil {
		return err
	}
	// TODO: implement STATUS here
	return nil
}

func createVethPair(containerId string) (string, string) {
	id := containerId[len(containerId)-8:]
	ovsPort := "veth" + id
	containerPort := "veth" + id + "_p"
	cmd := exec.Command("ip", "link", "add", ovsPort, "type", "veth", "peer", "name", containerPort)
	err := cmd.Run()
	if err != nil {
		return "", ""
	}
	return ovsPort, containerPort
}

func setOvsPort(bridge string, port string, ofPortNum string) {
	cmd := exec.Command("ip", "link", "set", port, "up")
	err := cmd.Run()
	if err != nil {
		return
	}

	cmd = exec.Command("ovs-vsctl", "add-port", bridge, port, "--", "set", "interface", port, "ofport_request="+ofPortNum)
	err = cmd.Run()
	if err != nil {
		logger.Printf("set_ovs_port: %v", err)
		os.Exit(0)
	}
}

func setPodPort(port string, podNetNamespace string, ip string, cidr string, gw string) {
	// Usa nsenter per eseguire i comandi all'interno del network namespace del pod
	//==================================================================================================================
	//"sposta" la veth_p dal nodo host al network namespace del pod:
	// es ip link set vethabfca85d_p netns /var/run/netns/cni-dadb64d0-695e-856a-4141-204e61c9e778
	cmd := exec.Command("ip", "link", "set", port, "netns", podNetNamespace)
	err := cmd.Run()
	if err != nil {
		return
	}
	//==================================================================================================================
	// Abilita interfaccia
	cmd = exec.Command("nsenter", "--net="+podNetNamespace, "ip", "link", "set", port, "up")
	err = cmd.Run()
	if err != nil {
		return
	}
	//==================================================================================================================
	// Aggiungi IP all'interfaccia
	cmd = exec.Command("nsenter", "--net="+podNetNamespace, "ip", "addr", "add", ip+"/"+cidr, "dev", port)
	err = cmd.Run()
	if err != nil {
		return
	}
	//==================================================================================================================
	// Aggiungi la rotta predefinita
	cmd = exec.Command("nsenter", "--net="+podNetNamespace, "ip", "route", "add", "default", "via", gw)
	err = cmd.Run()
	if err != nil {
		return
	}
	//==================================================================================================================
	// Disable offload for the specified interface 	ip netns exec $PID ethtool --offload $PORTNAME rx off tx off
	// va installato ethtool nel container...
	cmd = exec.Command("nsenter", "--net="+podNetNamespace, "ethtool", "--offload", port, "rx", "off", "tx", "off")
	err = cmd.Run()
	if err != nil {
		return
	}

}

func arrbyteToString(bs []byte) string {
	b := make([]byte, len(bs))
	for i, v := range bs {
		b[i] = byte(v)
	}
	return string(b)
}

func callIpamPlugin(config string, containerid string, netns string, ifname string, cnicommand string) ([]byte, error) {
	plugin := "host-local"
	path := "/opt/cni/bin"
	pluginPath := fmt.Sprintf("%s/%s", path, plugin)
	cmd := exec.Command(pluginPath)
	cmd.Env = append(os.Environ(),
		"CNI_COMMAND="+cnicommand, // ADD or DEL
		"CNI_CONTAINERID="+containerid,
		"CNI_NETNS="+netns,
		"CNI_IFNAME="+ifname,
		"CNI_PATH=/opt/cni/bin",
	)
	cmd.Stdin = strings.NewReader(config)
	// set output
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	// cmdRun
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to call plugin %s: %v, stderr: %s", plugin, err, stderr.String())
	}
	return out.Bytes(), nil
}

func getIpamNetData(config map[string]interface{}) (string, string, string, error) {
	ips, ok := config["ips"].([]interface{})
	if !ok || len(ips) == 0 {
		return "", "", "", fmt.Errorf("campo 'ips' mancante")
	}
	ipEntry, ok := ips[0].(map[string]interface{})
	if !ok {
		return "", "", "", fmt.Errorf("il primo elemento di 'ips' non è del tipo atteso map[string]interface{}")
	}
	address, ok := ipEntry["address"].(string)
	if !ok {
		return "", "", "", fmt.Errorf("campo 'address' mancante")
	}
	gateway, ok := ipEntry["gateway"].(string)
	if !ok {
		return "", "", "", fmt.Errorf("campo 'gateway' mancante")
	}
	parts := strings.Split(address, "/")
	if len(parts) != 2 {
		return "", "", "", fmt.Errorf("formato 'address' non valido: %s", address)
	}
	ip := parts[0]
	cidr := parts[1]
	return ip, cidr, gateway, nil
}
