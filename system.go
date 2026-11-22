package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type FS struct {
	Mount string  `json:"mount"`
	Label string  `json:"label"`
	Total uint64  `json:"total"`
	Used  uint64  `json:"used"`
	Free  uint64  `json:"free"`
	Pct   float64 `json:"pct"`
}

type Disk struct {
	Dev        string `json:"dev"`
	Model      string `json:"model"`
	Serial     string `json:"serial"`
	Size       uint64 `json:"size"`
	Role       string `json:"role"`
	Health     string `json:"health"` // PASSED / FAILED / ""
	TempC      int    `json:"temp_c"`
	PowerOnH   int    `json:"power_on_h"`
	Realloc    int    `json:"realloc"`
	Pending    int    `json:"pending"`
	Uncorrect  int    `json:"uncorrectable"`
	Sleeping   bool   `json:"sleeping"`
	Attributes []SmartAttr `json:"attributes,omitempty"`
}

type SmartAttr struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Value  string `json:"value"`
	Worst  string `json:"worst"`
	Thresh string `json:"thresh"`
	Raw    string `json:"raw"`
}

type Snapshot struct {
	Name   string    `json:"name"`
	Time   time.Time `json:"time"`
	Shares []string  `json:"shares"`
}

func statFS(mount, label string) (FS, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(mount, &st); err != nil {
		return FS{}, false
	}
	bs := uint64(st.Bsize)
	f := FS{Mount: mount, Label: label, Total: st.Blocks * bs, Free: st.Bavail * bs}
	f.Used = f.Total - st.Bfree*bs
	if f.Total > 0 {
		f.Pct = float64(f.Used) * 100 / float64(f.Used+f.Free)
	}
	return f, true
}

func filesystems(cfg *Config) []FS {
	var res []FS
	if f, ok := statFS(cfg.DataDir, "Datenvolume"); ok {
		res = append(res, f)
	}
	if f, ok := statFS("/", "System"); ok {
		res = append(res, f)
	}
	return res
}

func memInfo() (total, avail uint64) {
	b, _ := os.ReadFile("/proc/meminfo")
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			avail = v * 1024
		}
	}
	return
}

func cpuTemp() float64 {
	v, err := strconv.Atoi(readTrim("/sys/class/thermal/thermal_zone0/temp"))
	if err != nil {
		return 0
	}
	return float64(v) / 1000
}

var lastCPU struct {
	idle, total uint64
	at          time.Time
	pct         float64
}

// cpuPercent misst die CPU-Auslastung seit dem letzten Aufruf (bzw. kurz über 300 ms).
func cpuPercent() float64 {
	read := func() (idle, total uint64) {
		b, _ := os.ReadFile("/proc/stat")
		f := strings.Fields(strings.SplitN(string(b), "\n", 2)[0])
		for i, s := range f[1:] {
			v, _ := strconv.ParseUint(s, 10, 64)
			total += v
			if i == 3 || i == 4 {
				idle += v
			}
		}
		return
	}
	i1, t1 := read()
	if lastCPU.at.IsZero() || time.Since(lastCPU.at) > time.Minute {
		time.Sleep(300 * time.Millisecond)
		lastCPU.idle, lastCPU.total = i1, t1
		i1, t1 = read()
	}
	if t1 > lastCPU.total {
		lastCPU.pct = 100 * (1 - float64(i1-lastCPU.idle)/float64(t1-lastCPU.total))
	}
	lastCPU.idle, lastCPU.total, lastCPU.at = i1, t1, time.Now()
	return lastCPU.pct
}

func uptime() float64 {
	f := strings.Fields(readTrim("/proc/uptime"))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

type NetInfo struct {
	Iface string   `json:"iface"`
	MAC   string   `json:"mac"`
	IPs   []string `json:"ips"`
	Speed string   `json:"speed"`
	WOL   bool     `json:"wol"`
}

func netInfo(cfg *Config) NetInfo {
	n := NetInfo{Iface: cfg.NetIface}
	if ifc, err := net.InterfaceByName(cfg.NetIface); err == nil {
		n.MAC = ifc.HardwareAddr.String()
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				n.IPs = append(n.IPs, ipn.String())
			}
		}
	}
	if s := readTrim("/sys/class/net/" + cfg.NetIface + "/speed"); s != "" && s != "-1" {
		n.Speed = s + " Mbit/s"
	}
	// Wake-on-LAN aus dem Aus-Zustand: Realtek-Energietreiber muss "lan" als Weckquelle haben
	for _, l := range strings.Split(readTrim("/sys/kernel/suspend/wakeup"), "\n") {
		if strings.Contains(l, "*") && strings.Contains(l, "lan") {
			n.WOL = true
		}
	}
	return n
}

var snapRe = regexp.MustCompile(`^omv-(\d{4}-\d{2}-\d{2})_(\d{2})(\d{2})$`)

func snapshots(cfg *Config) []Snapshot {
	ents, _ := os.ReadDir(cfg.SnapshotDir)
	var res []Snapshot
	for _, e := range ents {
		m := snapRe.FindStringSubmatch(e.Name())
		if m == nil || !e.IsDir() {
			continue
		}
		t, err := time.ParseInLocation("2006-01-02 15:04", m[1]+" "+m[2]+":"+m[3], time.Local)
		if err != nil {
			continue
		}
		s := Snapshot{Name: e.Name(), Time: t}
		sub, _ := os.ReadDir(filepath.Join(cfg.SnapshotDir, e.Name()))
		for _, x := range sub {
			if x.IsDir() {
				s.Shares = append(s.Shares, x.Name())
			}
		}
		res = append(res, s)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Time.After(res[j].Time) })
	return res
}

// ---------- Datenträger / SMART ----------

func diskList(withAttrs bool) []Disk {
	ents, _ := os.ReadDir("/sys/block")
	var res []Disk
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, "sd") && !strings.HasPrefix(n, "nvme") {
			continue
		}
		sectors, _ := strconv.ParseUint(readTrim("/sys/block/"+n+"/size"), 10, 64)
		d := Disk{Dev: "/dev/" + n, Size: sectors * 512,
			Model: strings.TrimSpace(readTrim("/sys/block/" + n + "/device/vendor") + " " + readTrim("/sys/block/"+n+"/device/model"))}
		if d.Size < 4<<30 {
			d.Role = "System-Flash"
		} else {
			d.Role = "Daten"
			smartFill(&d, withAttrs)
		}
		res = append(res, d)
	}
	return res
}

func smartFill(d *Disk, withAttrs bool) {
	// -n standby: schlafende Platten nicht aufwecken
	out, _ := run(20*time.Second, "", "smartctl", "-n", "standby", "-i", "-H", "-A", d.Dev)
	if strings.Contains(out, "STANDBY") || strings.Contains(out, "SLEEP") {
		d.Sleeping = true
		return
	}
	inAttrs := false
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "Device Model:") || strings.HasPrefix(l, "Model Number:"):
			d.Model = strings.TrimSpace(strings.SplitN(l, ":", 2)[1])
		case strings.HasPrefix(l, "Serial Number:"):
			d.Serial = strings.TrimSpace(strings.SplitN(l, ":", 2)[1])
		case strings.Contains(l, "overall-health"):
			if strings.Contains(l, "PASSED") {
				d.Health = "PASSED"
			} else {
				d.Health = "FAILED"
			}
		case strings.HasPrefix(l, "ID#"):
			inAttrs = true
		case inAttrs:
			f := strings.Fields(l)
			if len(f) < 10 {
				inAttrs = false
				continue
			}
			id, _ := strconv.Atoi(f[0])
			raw := strings.Join(f[9:], " ")
			rv, _ := strconv.Atoi(strings.Fields(raw)[0])
			switch id {
			case 194, 190:
				if d.TempC == 0 {
					d.TempC = rv
				}
			case 9:
				d.PowerOnH = rv
			case 5:
				d.Realloc = rv
			case 197:
				d.Pending = rv
			case 198:
				d.Uncorrect = rv
			}
			if withAttrs {
				d.Attributes = append(d.Attributes, SmartAttr{ID: id, Name: f[1], Value: f[3], Worst: f[4], Thresh: f[5], Raw: raw})
			}
		}
	}
}

// ---------- API ----------

func (app *App) apiDashboard(w http.ResponseWriter, r *http.Request, s *session) {
	total, avail := memInfo()
	load := strings.Fields(readTrim("/proc/loadavg"))
	host, _ := os.Hostname()
	snaps := snapshots(app.cfg)
	var last *Snapshot
	if len(snaps) > 0 {
		last = &snaps[0]
	}
	backupOK := last != nil && time.Since(last.Time) < time.Duration(app.cfg.BackupMaxAgeH)*time.Hour
	writeJSON(w, 200, map[string]any{
		"hostname": host, "kernel": readTrim("/proc/sys/kernel/osrelease"),
		"model":   readTrim("/proc/device-tree/model"), "arch": runtime.GOARCH, "version": version,
		"uptime":  uptime(), "load": load, "cpu_pct": cpuPercent(), "cpu_temp": cpuTemp(),
		"cpus":    runtime.NumCPU(), "mem_total": total, "mem_avail": avail,
		"fs":      filesystems(app.cfg), "disks": diskList(false), "net": netInfo(app.cfg),
		"backup":  map[string]any{"source": app.cfg.BackupSource, "last": last, "count": len(snaps), "ok": backupOK, "max_age_h": app.cfg.BackupMaxAgeH},
		"services": services(), "time": time.Now(),
	})
}

func (app *App) apiDisks(w http.ResponseWriter, r *http.Request, s *session) {
	writeJSON(w, 200, map[string]any{"disks": diskList(true), "fs": filesystems(app.cfg)})
}
