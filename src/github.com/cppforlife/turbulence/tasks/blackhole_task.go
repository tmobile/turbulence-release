// See https://wiki.centos.org/HowTos/Network/IPTables for a good iptables tutorial

package tasks

import (
	"regexp"
	"strings"

	bosherr "github.com/cloudfoundry/bosh-utils/errors"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
	boshsys "github.com/cloudfoundry/bosh-utils/system"
)

type BlackholeOptions struct {
	Type    string
	Timeout string // Times may be suffixed with ms,s,m,h

	Targets []BlackholeTarget
}

var ipPattern = regexp.MustCompile(`(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})(/\d{0,2})?`)
var portPattern = regexp.MustCompile(`\d+(:\d+)?$`)

// BlackholeTarget defines a rule for iptables. Each rule must contain one of {Host, DstPorts, SrcPorts}.
// If DstPorts or SrcPorts ports are included without a Host, then those ports will be blocked for all hosts.
// If Host is included without DstPorts or SrcPorts, then all traffic to/from those hosts will be blocked.
type BlackholeTarget struct {
	// Optional host to block, can specify an address such as "10.34.4.60", an address block such as "192.168.0.0/24",
	// or a domain name such as "google.com" which will be resolved to an Ip.
	Host string

	// Optional direction to block traffic, must be in the set {INPUT, OUTPUT, BOTH}. Defaults to "BOTH".
	Direction string

	// Optional protocol to block, must be in the set {udp, tcp, icmp, all}. Defaults to "all".
	Protocol string

	// Optional "dport" or destination port(s) to block. Specify a single port such as "8080" or a range such as "4530:6740".
	DstPorts string

	// Optional "sport" or source port(s) to block. Specify a single port such as "8080" or a range such as "4530:6740".
	SrcPorts string
}

func (BlackholeOptions) _private() {}

type BlackholeTask struct {
	cmdRunner boshsys.CmdRunner
	opts      BlackholeOptions
	logger	  boshlog.Logger
}

func NewBlackholeTask(
	cmdRunner boshsys.CmdRunner,
	opts BlackholeOptions,
	logger boshlog.Logger,
) BlackholeTask {
	return BlackholeTask{cmdRunner, opts, logger}
}

func (t BlackholeTask) Execute(stopCh chan struct{}) error {
	timeoutCh, err := NewOptionalTimeoutCh(t.opts.Timeout)
	if err != nil {
		return err
	}

	rules, err := t.rules()
	if err != nil {
		return err
	}

	for _, r := range rules {
		err := t.iptables("-A", r)
		if err != nil {
			return err
		}
	}

	select {
	case <-timeoutCh:
	case <-stopCh:
	}

	for _, r := range rules {
		err := t.iptables("-D", r)
		if err != nil {
			return err
		}
	}

	return nil
}

func (t BlackholeTask) rules() ([]string, error) {
	var rules []string
	for _, target := range t.opts.Targets {
		if target.Host == "" && target.DstPorts == "" && target.SrcPorts == "" {
			return nil, bosherr.Error("Must specify at least one of Host, DstPorts, and or SrcPorts.")
		}

		var hosts []string
		var direction, protocol, dports, sports string
		
		if target.Host == "" {
			hosts = nil
		} else if ipPattern.MatchString(target.Host) {
			hosts = ipPattern.FindAllString(target.Host, -1)
		} else {
			var err error
			hosts, err = t.dig(target.Host)
			
			if err != nil {
				return nil, err
			}
		}

		switch strings.ToUpper(target.Direction) {
		case "":
			direction = ""
		case "INPUT":
			direction = "INPUT"
		case "OUTPUT":
			direction = "OUTPUT"
		case "BOTH":
			direction = ""
		default:
			return nil, bosherr.Errorf("Invalid direction '%v', must be one of {INPUT, OUTPUT, BOTH} or blank.", target.Direction)
		}

		switch strings.ToLower(target.Protocol) {
		case "":
			protocol = "all"
		case "tcp":
			protocol = "tcp"
		case "udp":
			protocol = "udp"
		case "icmp":
			protocol = "icmp"
		case "all":
			protocol = "all"
		default:
			return nil, bosherr.Errorf("Invalid protocol '%v', must be one of {tcp, udp, icmp, all} or blank.", target.Protocol)
		}

		if target.DstPorts == "" {
			dports = ""
		} else if portPattern.MatchString(target.DstPorts) {
			dports = target.DstPorts
		} else {
			return nil, bosherr.Errorf("Invalid destination port specified %v", target.DstPorts)
		}

		if target.SrcPorts == "" {
			sports = ""
		} else if portPattern.MatchString(target.SrcPorts) {
			sports = target.SrcPorts
		} else {
			return nil, bosherr.Errorf("Invalid destination port specified %v", target.SrcPorts)
		}
		
		
		if direction == "" || direction == "INPUT" {
			command := "INPUT"
			
			if hosts != nil {
				command += " -s "
				for i, ip := range hosts {
					if i > 0 { command += ","}
					command += ip
				}
			}

			command += " -p " + protocol

			if dports != "" {
				command += " -dport " + dports
			}
			
			if sports != "" {
				command += " -sport " + sports
			}

			command += " -j DROP"
			rules = append(rules, command)
		}

		if direction == "" || direction == "OUTPUT" {
			command := "OUTPUT"

			if hosts != nil {
				command += " -d "
				for i, ip := range hosts {
					if i > 0 { command += ","}
					command += ip
				}
			}

			command += " -p " + protocol

			if dports != "" {
				command += " -dport " + dports
			}
			
			if sports != "" {
				command += " -sport " + sports
			}

			command += " -j DROP"
			rules = append(rules, command)
		}
	}

	return rules, nil
}

func (t BlackholeTask) dig(hostname string) ([]string, error) {
	args := []string{"+short", hostname}
	output, _, _, err := t.cmdRunner.RunCommand("dig", args...)
	if err != nil {
		return nil, bosherr.WrapError(err, "resolving host name")
	}

	ips := ipPattern.FindAllString(output, -1)
	if ips == nil {
		return nil, bosherr.Errorf("No IPs found for host %v", hostname)
	}
	
	return ips, nil
}

func (t BlackholeTask) iptables(action, rule string) error {
	args := append([]string{action}, strings.Split(rule, " ")...)

	_, _, _, err := t.cmdRunner.RunCommand("iptables", args...)
	if err != nil {
		return bosherr.WrapError(err, "Shelling out to iptables")
	}

	return nil
}