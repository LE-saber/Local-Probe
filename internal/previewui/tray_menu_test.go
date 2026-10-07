package previewui

import "testing"

func TestTrayMenuUsesActionForCurrentWindowVisibility(t *testing.T) {
	visible := buildTrayMenuModel(true, FontScale150)
	if got := visible.Items[0].Label; got != "隐藏主界面" {
		t.Fatalf("visible window label = %q, want hide action", got)
	}
	hidden := buildTrayMenuModel(false, FontScale150)
	if got := hidden.Items[0].Label; got != "显示主界面" {
		t.Fatalf("hidden window label = %q, want show action", got)
	}
	if visible.Items[0].Command != menuOpen || hidden.Items[0].Command != menuOpen {
		t.Fatal("window visibility actions must share the toggle command")
	}
}

func TestTrayMenuContainsRequiredChineseActions(t *testing.T) {
	model := buildTrayMenuModel(false, FontScale150)
	want := []string{"显示主界面", "刷新状态", "导出诊断", "关于 Local-Probe", "字号", "退出 Preview"}
	for _, label := range want {
		found := false
		for _, item := range model.Items {
			if item.Label == label {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("tray menu missing %q", label)
		}
	}
}

func TestTrayFontMenuMarksOnlyNormalizedCurrentScale(t *testing.T) {
	items := trayFontMenuItems(101)
	checked := 0
	for _, item := range items {
		if item.Checked {
			checked++
			if item.Command != menuFont150 {
				t.Fatalf("normalized default checked command = %d, want %d", item.Command, menuFont150)
			}
		}
	}
	if checked != 1 {
		t.Fatalf("checked font items = %d, want exactly one", checked)
	}
}

func TestTrayFontCommandRoundTrip(t *testing.T) {
	for _, item := range trayFontMenuItems(FontScale200) {
		scale, ok := trayFontScaleForCommand(item.Command)
		if !ok {
			t.Fatalf("font command %d is not mapped", item.Command)
		}
		if scale != NormalizeFontScale(FontScale(scale)) {
			t.Fatalf("font command %d mapped to invalid scale %d", item.Command, scale)
		}
	}
	if _, ok := trayFontScaleForCommand(menuRefresh); ok {
		t.Fatal("non-font command unexpectedly mapped to font scale")
	}
}

func TestTrayEventCodeAcceptsVersion4PackedLParam(t *testing.T) {
	const event = uint32(0x0205)
	if got := trayEventCode((17 << 16) | event); got != event {
		t.Fatalf("trayEventCode() = %#x, want %#x", got, event)
	}
}
