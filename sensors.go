package nara

import (
	"errors"
	"github.com/shirou/gopsutil/host"
	"github.com/shirou/gopsutil/load"
	"net"
	"runtime"
	"strings"
	"time"
)

type HostStats struct {
	Uptime  uint64
	LoadAvg float64
}

func (ln *LocalNara) updateHostStatsForever() {
	for {
		ln.updateHostStats()
		time.Sleep(5 * time.Second)
	}
}

func (ln *LocalNara) updateHostStats() {
	uptime, _ := host.Uptime()
	ln.Me.Status.HostStats.Uptime = uptime

	load, _ := load.Avg()
	loadavg := load.Load1 / float64(runtime.NumCPU())
	ln.Me.Status.HostStats.LoadAvg = loadavg

	if ln.forceChattiness >= 0 && ln.forceChattiness <= 100 {
		ln.Me.Status.Chattiness = int64(ln.forceChattiness)
	} else {
		if loadavg < 1 {
			ln.Me.Status.Chattiness = int64((1 - loadavg) * 100)
		} else {
			ln.Me.Status.Chattiness = 0
		}
	}
}

// https://stackoverflow.com/questions/23558425/how-do-i-get-the-local-ip-address-in-go
func externalIP() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue // interface down
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue // loopback interface
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return "", err
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			ip = ip.To4()
			if ip == nil {
				continue // not an ipv4 address
			}

			// skip non-tailscale IPs
			if !strings.HasPrefix(ip.String(), "100.") {
				continue
			}

			return ip.String(), nil
		}
	}
	return "", errors.New("are you connected to the network?")
}
