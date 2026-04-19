package builtin

import (
	"context"
	"strings"
	"testing"

	"github.com/i4Edu/netmantle/internal/drivers"
	"github.com/i4Edu/netmantle/internal/drivers/fakesession"
)

func TestCiscoIOSFetchConfig(t *testing.T) {
	d, err := drivers.Get("cisco_ios")
	if err != nil {
		t.Fatal(err)
	}
	sess := fakesession.New(map[string]string{
		"terminal length 0":   "",
		"show running-config": "Building configuration...\nCurrent configuration : 1234 bytes\n!\nhostname r1\n!\nend\n",
		"show startup-config": "!\nhostname r1\n!\nend\n",
	})
	arts, err := d.FetchConfig(context.Background(), sess)
	if err != nil {
		t.Fatalf("FetchConfig: %v", err)
	}
	if len(arts) != 2 {
		t.Fatalf("want 2 artefacts, got %d", len(arts))
	}
	if arts[0].Name != "running-config" {
		t.Errorf("name: %s", arts[0].Name)
	}
	if strings.Contains(string(arts[0].Content), "Building configuration") {
		t.Errorf("chrome not stripped: %s", arts[0].Content)
	}
	if !strings.Contains(string(arts[0].Content), "hostname r1") {
		t.Errorf("payload missing: %s", arts[0].Content)
	}
}

func TestCiscoIOSStartupOptional(t *testing.T) {
	d, _ := drivers.Get("cisco_ios")
	sess := fakesession.New(map[string]string{
		"terminal length 0":   "",
		"show running-config": "hostname r2\n",
		// no startup-config response -> error
	})
	arts, err := d.FetchConfig(context.Background(), sess)
	if err != nil {
		t.Fatalf("expected success when startup unavailable: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("want 1 artefact, got %d", len(arts))
	}
}

func TestAristaEOS(t *testing.T) {
	d, _ := drivers.Get("arista_eos")
	sess := fakesession.New(map[string]string{
		"terminal length 0":   "",
		"show running-config": "hostname sw1\n",
	})
	arts, err := d.FetchConfig(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 || arts[0].Name != "running-config" {
		t.Fatalf("bad: %+v", arts)
	}
}

func TestGenericSSHFallback(t *testing.T) {
	d, _ := drivers.Get("generic_ssh")
	sess := fakesession.New(map[string]string{
		"show running-config": "hello\n",
	})
	arts, err := d.FetchConfig(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	if string(arts[0].Content) != "hello\n" {
		t.Fatalf("got %q", arts[0].Content)
	}
}

func TestStripIOSChrome(t *testing.T) {
	in := "Building configuration...\nCurrent configuration : 1 bytes\n!\nhostname x\n"
	got := stripIOSChrome(in)
	if strings.Contains(got, "Building") || strings.Contains(got, "Current configuration ") {
		t.Errorf("not stripped: %q", got)
	}
	if !strings.Contains(got, "hostname x") {
		t.Errorf("payload lost: %q", got)
	}
}

func TestExtendedVendors(t *testing.T) {
	cases := []struct {
		driver  string
		session map[string]string
		artName string
		wantSub string
	}{
		{
			driver: "cisco_nxos",
			session: map[string]string{
				"terminal length 0":   "",
				"show running-config": "hostname nxos1\n",
				"show startup-config": "hostname nxos1\n",
			},
			artName: "running-config",
			wantSub: "hostname nxos1",
		},
		{
			driver: "cisco_iosxr",
			session: map[string]string{
				"terminal length 0":   "",
				"show running-config": "hostname xr1\n",
			},
			artName: "running-config",
			wantSub: "hostname xr1",
		},
		{
			driver: "nokia_sros",
			session: map[string]string{
				"environment no more":  "",
				"admin display-config": "# TiMOS\nconfigure system name \"sr1\"\nexit all\n",
			},
			artName: "running-config",
			wantSub: `system name "sr1"`,
		},
		{
			driver: "bdcom_os",
			session: map[string]string{
				"terminal length 0":   "",
				"show running-config": "hostname olt-bdcom\n",
			},
			artName: "running-config",
			wantSub: "hostname olt-bdcom",
		},
		{
			driver: "vsol_os",
			session: map[string]string{
				"enable":              "",
				"terminal length 0":   "",
				"show running-config": "hostname olt-vsol\n",
			},
			artName: "running-config",
			wantSub: "hostname olt-vsol",
		},
		{
			driver: "dbc_os",
			session: map[string]string{
				"enable":              "",
				"terminal length 0":   "",
				"show running-config": "hostname olt-dbc\n",
			},
			artName: "running-config",
			wantSub: "hostname olt-dbc",
		},
	}
	for _, tc := range cases {
		t.Run(tc.driver, func(t *testing.T) {
			d, err := drivers.Get(tc.driver)
			if err != nil {
				t.Fatal(err)
			}
			arts, err := d.FetchConfig(context.Background(), fakesession.New(tc.session))
			if err != nil {
				t.Fatalf("FetchConfig: %v", err)
			}
			if len(arts) == 0 {
				t.Fatalf("no artefacts")
			}
			if arts[0].Name != tc.artName {
				t.Errorf("name=%s want %s", arts[0].Name, tc.artName)
			}
			if !strings.Contains(string(arts[0].Content), tc.wantSub) {
				t.Errorf("payload missing %q in %q", tc.wantSub, arts[0].Content)
			}
		})
	}
}

func TestMikrotikExport(t *testing.T) {
	d, _ := drivers.Get("mikrotik_routeros")
	sess := fakesession.New(map[string]string{
		"/export": "/system identity\nset name=mt1\n",
	})
	arts, err := d.FetchConfig(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 || arts[0].Name != "export" {
		t.Fatalf("bad: %+v", arts)
	}
	if !strings.Contains(string(arts[0].Content), "name=mt1") {
		t.Errorf("payload: %q", arts[0].Content)
	}
}

func TestFortiOS(t *testing.T) {
	d, err := drivers.Get("fortios")
	if err != nil {
		t.Fatal(err)
	}
	sess := fakesession.New(map[string]string{
		"config system console":   "",
		"set output standard":     "",
		"end":                     "",
		"show full-configuration": "config system global\n    set hostname fw1\nend\n",
	})
	arts, err := d.FetchConfig(context.Background(), sess)
	if err != nil {
		t.Fatalf("FetchConfig: %v", err)
	}
	if len(arts) != 1 || arts[0].Name != "running-config" {
		t.Fatalf("bad: %+v", arts)
	}
	if !strings.Contains(string(arts[0].Content), "hostname fw1") {
		t.Errorf("payload missing hostname: %q", arts[0].Content)
	}
}

func TestFortiOSPagerOptional(t *testing.T) {
	// Verify the driver succeeds even when the pager-disable commands fail
	// (e.g. read-only account).
	d, _ := drivers.Get("fortios")
	sess := fakesession.New(map[string]string{
		// pager commands not registered -> fakesession returns error, driver ignores
		"show full-configuration": "config system global\n    set hostname fw2\nend\n",
	})
	arts, err := d.FetchConfig(context.Background(), sess)
	if err != nil {
		t.Fatalf("expected success when pager commands fail: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("want 1 artefact, got %d", len(arts))
	}
}

func TestPaloAltoPANOS(t *testing.T) {
	d, err := drivers.Get("paloalto_panos")
	if err != nil {
		t.Fatal(err)
	}
	sess := fakesession.New(map[string]string{
		"set cli pager off":   "",
		"show config running": "<config><devices><entry><deviceconfig><system><hostname>pa1</hostname></system></deviceconfig></entry></devices></config>\n",
	})
	arts, err := d.FetchConfig(context.Background(), sess)
	if err != nil {
		t.Fatalf("FetchConfig: %v", err)
	}
	if len(arts) != 1 || arts[0].Name != "running-config" {
		t.Fatalf("bad: %+v", arts)
	}
	if !strings.Contains(string(arts[0].Content), "pa1") {
		t.Errorf("payload missing hostname: %q", arts[0].Content)
	}
}

func TestHuaweiVRP(t *testing.T) {
	d, err := drivers.Get("huawei_vrp")
	if err != nil {
		t.Fatal(err)
	}
	sess := fakesession.New(map[string]string{
		"screen-length 0 temporary":     "",
		"display current-configuration": "#\nsysname vrp1\n#\nreturn\n",
	})
	arts, err := d.FetchConfig(context.Background(), sess)
	if err != nil {
		t.Fatalf("FetchConfig: %v", err)
	}
	if len(arts) != 1 || arts[0].Name != "running-config" {
		t.Fatalf("bad: %+v", arts)
	}
	if !strings.Contains(string(arts[0].Content), "sysname vrp1") {
		t.Errorf("payload missing sysname: %q", arts[0].Content)
	}
}
