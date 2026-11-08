package romset

import "testing"

func TestParseRomName(t *testing.T) {
	for name, want := range map[string]Kind{
		"pin2000_50070_0260_game.rom":     PartGame,
		"PIN2000_50070_0120_BOOTDATA.ROM": PartBootData,
		"im_flsh0.rom":                    PartImFlsh0,
		"pin2000_50070_0191_sf.rom":       PartSoundFlash,
	} {
		_, k, ok := ParseRomName(name)
		if !ok || k != want {
			t.Errorf("%s: %v %v", name, k, ok)
		}
	}
	if _, _, ok := ParseRomName("gamelist.txt"); ok {
		t.Error("gamelist.txt is not a rom")
	}
	if g, v := PrefixInfo("pin2000_50070_0260_"); g != "50070" || v != "0260" {
		t.Error(g, v)
	}
}

func TestVersions(t *testing.T) {
	bd := &BootData{VersionMajor: 1, VersionMinor: 1, SystemMajor: 0x2031, SystemMinor: 0x3337, GameMajor: 1, GameMinor: 8}
	if bd.SystemVersion() != "1.37" || bd.GameVersion() != "1.8" {
		t.Error(bd.SystemVersion(), bd.GameVersion())
	}
	s := &Set{FileVersion: "0191", Boot: bd}
	if s.Version() != "1.91" {
		t.Error(s.Version())
	}
}

func TestSum32(t *testing.T) {
	if Sum32([]byte{1, 0, 0, 0, 2, 0, 0, 0, 3}, 3) != 6 {
		t.Error("sum with partial tail")
	}
}
