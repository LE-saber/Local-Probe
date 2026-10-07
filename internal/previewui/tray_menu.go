package previewui

// trayMenuKind describes the small amount of structure the native Windows
// menu renderer needs. Keeping the menu model platform-neutral makes labels,
// command IDs, and checked state testable without creating a Win32 window.
type trayMenuKind uint8

const (
	trayMenuAction trayMenuKind = iota
	trayMenuSeparator
	trayMenuSubmenu
)

type trayMenuItem struct {
	Label   string
	Command uint32
	Kind    trayMenuKind
	Checked bool
}

type trayMenuModel struct {
	Items     []trayMenuItem
	FontItems []trayMenuItem
}

const (
	menuOpen    = 2001
	menuRefresh = 2002
	menuExport  = 2003
	menuAbout   = 2004
	menuExit    = 2005
	menuFont    = 2006

	menuFont100 = 2010
	menuFont125 = 2011
	menuFont150 = 2012
	menuFont175 = 2013
	menuFont200 = 2014
)

// buildTrayMenuModel returns the complete menu state for one invocation.
// Window visibility is sampled when the menu opens, so the first entry always
// describes the action that will be performed by selecting it.
func buildTrayMenuModel(windowVisible bool, scale FontScale) trayMenuModel {
	fontItems := trayFontMenuItems(scale)
	return trayMenuModel{
		Items: []trayMenuItem{
			{Label: trayWindowToggleLabel(windowVisible), Command: menuOpen},
			{Label: "刷新状态", Command: menuRefresh},
			{Label: "导出诊断", Command: menuExport},
			{Label: "关于 Local-Probe", Command: menuAbout},
			{Kind: trayMenuSeparator},
			{Label: "字号", Command: menuFont, Kind: trayMenuSubmenu},
			{Kind: trayMenuSeparator},
			{Label: "退出 Preview", Command: menuExit},
		},
		FontItems: fontItems,
	}
}

func trayWindowToggleLabel(windowVisible bool) string {
	if windowVisible {
		return "隐藏主界面"
	}
	return "显示主界面"
}

func trayFontMenuItems(scale FontScale) []trayMenuItem {
	scale = NormalizeFontScale(scale)
	steps := []struct {
		scale   FontScale
		command uint32
	}{
		{FontScale100, menuFont100},
		{FontScale125, menuFont125},
		{FontScale150, menuFont150},
		{FontScale175, menuFont175},
		{FontScale200, menuFont200},
	}
	items := make([]trayMenuItem, 0, len(steps))
	for _, step := range steps {
		items = append(items, trayMenuItem{
			Label:   FontScaleLabel(step.scale),
			Command: step.command,
			Kind:    trayMenuAction,
			Checked: step.scale == scale,
		})
	}
	return items
}

func trayFontScaleForCommand(command uint32) (FontScale, bool) {
	switch command {
	case menuFont100:
		return FontScale100, true
	case menuFont125:
		return FontScale125, true
	case menuFont150:
		return FontScale150, true
	case menuFont175:
		return FontScale175, true
	case menuFont200:
		return FontScale200, true
	default:
		return 0, false
	}
}

// trayEventCode extracts the notification code used by NOTIFYICON_VERSION_4.
// The shell stores the icon identifier in the high word of lParam.
func trayEventCode(value uint32) uint32 {
	return value & 0xffff
}
