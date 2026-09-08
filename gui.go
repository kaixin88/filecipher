//go:build windows

package main

// GUI 界面: 基于 windigo (纯 Go Win32 绑定, 无 CGO, 启动毫秒级)
// 布局: 固定坐标(简化); 后台 goroutine 处理 + 主线程 timer 轮询进度

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"unicode/utf16"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

var (
	giProgress atomic.Int64
	giStatus   atomic.Int32
	giErrMsg   atomic.Value
	giCurName  atomic.Value
)

const (
	stIdle    = 0
	stRun     = 1
	stDoneOK  = 2
	stDoneErr = 3
)

type fileList struct {
	items []string
	mu    atomic.Int32
}

func (f *fileList) add(paths []string) int {
	for !f.mu.CompareAndSwap(0, 1) {
	}
	defer f.mu.Store(0)
	added := 0
	for _, p := range paths {
		dup := false
		for _, e := range f.items {
			if e == p {
				dup = true
				break
			}
		}
		if !dup {
			f.items = append(f.items, p)
			added++
		}
	}
	return added
}

func (f *fileList) removeSelected(idxs []int) {
	if len(idxs) == 0 {
		return
	}
	for !f.mu.CompareAndSwap(0, 1) {
	}
	defer f.mu.Store(0)
	del := make(map[int]bool)
	for _, i := range idxs {
		del[i] = true
	}
	out := make([]string, 0, len(f.items))
	for i, e := range f.items {
		if !del[i] {
			out = append(out, e)
		}
	}
	f.items = out
}

func (f *fileList) clear() {
	for !f.mu.CompareAndSwap(0, 1) {
	}
	defer f.mu.Store(0)
	f.items = nil
}

func (f *fileList) snapshot() []string {
	for !f.mu.CompareAndSwap(0, 1) {
	}
	defer f.mu.Store(0)
	out := make([]string, len(f.items))
	copy(out, f.items)
	return out
}

type appUI struct {
	wnd      *ui.Main
	lv       *ui.ListView
	edtOut   *ui.Edit
	edtPw1   *ui.Edit
	edtPw2   *ui.Edit
	ckShow   *ui.CheckBox
	btnAuto  *ui.Button
	btnEnc   *ui.Button
	btnDec   *ui.Button
	pb       *ui.ProgressBar
	lblStat  *ui.Static
	fl       *fileList
	busy     atomic.Bool
}

// 隐藏控制台窗口: 若程序以 console 子系统编译且被双击, Windows 会
// 新建一个黑色控制台窗口, 这里在 GUI 启动时立即销毁它 (双击零黑框)
func hideConsole() {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("FreeConsole")
	_, _, _ = proc.Call()
}

func runGUI() {
	hideConsole() // 双击启动时消除黑色控制台窗口
	runtime.LockOSThread()
	a := &appUI{fl: &fileList{}}

	a.wnd = ui.NewMain(
		ui.OptsMain().
			Title(VERSION_INFO).
			Size(700, 560).
			Style(co.WS_CAPTION|co.WS_SYSMENU|co.WS_CLIPCHILDREN|co.WS_BORDER|co.WS_VISIBLE|co.WS_MINIMIZEBOX),
	)

	// 提示 (原加密/解密单选位置)
	ui.NewStatic(a.wnd, ui.OptsStatic().
		Text("拖入 / Ctrl+V 粘贴文件 · 智能处理自动识别(.fcp 解密)").
		Position(20, 24))

	// 文件列表
	a.lv = ui.NewListView(a.wnd,
		ui.OptsListView().
			Position(20, 50).
			Size(540, 230).
			CtrlStyle(co.LVS_REPORT|co.LVS_SHOWSELALWAYS).
			CtrlExStyle(co.LVS_EX_FULLROWSELECT|co.LVS_EX_GRIDLINES).
			Column("文件路径", 540), // 必须在 Opts 里加列: 窗口创建后 windigo 会自动调用 AddCol
	)

	btnAdd := ui.NewButton(a.wnd, ui.OptsButton().
		Text("添加文件...").
		Position(575, 50).
		Width(100).Height(32))
	btnAdd.On().BnClicked(func() { a.onAdd() })

	btnRm := ui.NewButton(a.wnd, ui.OptsButton().
		Text("移除选中").
		Position(575, 87).
		Width(100).Height(32))
	btnRm.On().BnClicked(func() { a.onRemove() })

	btnClr := ui.NewButton(a.wnd, ui.OptsButton().
		Text("清空列表").
		Position(575, 124).
		Width(100).Height(32))
	btnClr.On().BnClicked(func() { a.onClear() })

	// 从剪贴板粘贴复制的文件 (等效 Ctrl+V)
	btnPaste := ui.NewButton(a.wnd, ui.OptsButton().
		Text("粘贴文件").
		Position(575, 161).
		Width(100).Height(32))
	btnPaste.On().BnClicked(func() { a.onPaste() })

	// 输出目录
	ui.NewStatic(a.wnd, ui.OptsStatic().Text("输出目录:").Position(20, 295))
	a.edtOut = ui.NewEdit(a.wnd, ui.OptsEdit().
		Position(90, 292).
		Width(440).Height(24))
	btnBrowse := ui.NewButton(a.wnd, ui.OptsButton().
		Text("浏览...").
		Position(540, 289).
		Width(80).Height(28))
	btnBrowse.On().BnClicked(func() { a.onBrowse() })
	ui.NewStatic(a.wnd, ui.OptsStatic().
		Text("(默认=源文件所在目录)").
		Position(625, 295))

	// 密码 (默认 123, 不改直接用)
	ui.NewStatic(a.wnd, ui.OptsStatic().Text("密码:").Position(20, 332))
	a.edtPw1 = ui.NewEdit(a.wnd, ui.OptsEdit().
		Position(70, 329).
		Width(220).Height(24).
		CtrlStyle(co.ES_PASSWORD|co.ES_AUTOHSCROLL).
		Text("123"))

	ui.NewStatic(a.wnd, ui.OptsStatic().Text("确认:").Position(305, 332))
	a.edtPw2 = ui.NewEdit(a.wnd, ui.OptsEdit().
		Position(355, 329).
		Width(220).Height(24).
		CtrlStyle(co.ES_PASSWORD|co.ES_AUTOHSCROLL).
		Text("123"))

	a.ckShow = ui.NewCheckBox(a.wnd, ui.OptsCheckBox().
		Text("显示").
		Position(585, 332))
	a.ckShow.On().BnClicked(func() {
		const EM_SETPASSWORDCHAR = 0x00CC
		var ch uintptr
		if a.ckShow.IsChecked() {
			ch = 0
		} else {
			ch = uintptr('*')
		}
		a.edtPw1.Hwnd().SendMessage(EM_SETPASSWORDCHAR, win.WPARAM(ch), win.LPARAM(0))
		a.edtPw2.Hwnd().SendMessage(EM_SETPASSWORDCHAR, win.WPARAM(ch), win.LPARAM(0))
		a.edtPw1.Hwnd().InvalidateRect(nil, true)
		a.edtPw2.Hwnd().InvalidateRect(nil, true)
	})

	ui.NewStatic(a.wnd, ui.OptsStatic().
		Text("密码请务必牢记,遗忘后文件无法恢复!").
		Position(20, 365))

	// 操作: 智能处理(自动识别) + 加密/解密 独立按钮
	a.btnAuto = ui.NewButton(a.wnd, ui.OptsButton().
		Text("智 能 处 理").
		Position(20, 395).
		Width(130).Height(34))
	a.btnAuto.On().BnClicked(func() { a.onStart(modeAuto) })

	a.btnEnc = ui.NewButton(a.wnd, ui.OptsButton().
		Text("开 始 加 密").
		Position(160, 395).
		Width(125).Height(34))
	a.btnEnc.On().BnClicked(func() { a.onStart(modeEnc) })

	a.btnDec = ui.NewButton(a.wnd, ui.OptsButton().
		Text("开 始 解 密").
		Position(295, 395).
		Width(125).Height(34))
	a.btnDec.On().BnClicked(func() { a.onStart(modeDec) })

	a.pb = ui.NewProgressBar(a.wnd, ui.OptsProgressBar().
		Position(430, 395).
		Size(250, 26).
		Range(0, 100))

	a.lblStat = ui.NewStatic(a.wnd, ui.OptsStatic().
		Text("就绪 - 拖入 / Ctrl+V 粘贴添加文件, 密码默认 123").
		Position(20, 438))

	// timer 轮询进度
	a.wnd.On().WmCreate(func(p ui.WmCreate) int {
		dragAcceptFiles(a.wnd.Hwnd(), true) // 传统 WM_DROPFILES 拖放, 无需 OLE
		a.wnd.Hwnd().SetTimer(1, 100)
		// 此时所有子控件已创建 (beforeUserEvents 先于 userEvents),
		// 显式设置列表文字/背景色 (避免部分会话下文字与背景同色)
		a.lv.Hwnd().SendMessage(co.LVM_SETBKCOLOR, 0, win.LPARAM(0xFFFFFF))
		a.lv.Hwnd().SendMessage(co.LVM_SETTEXTBKCOLOR, 0, win.LPARAM(0xFFFFFF))
		a.lv.Hwnd().SendMessage(co.LVM_SETTEXTCOLOR, 0, win.LPARAM(0))
		return 0
	})
	a.wnd.On().WmTimer(1, func() { a.pollProgress() })
	a.wnd.On().WmDropFiles(func(p ui.WmDropFiles) { a.onDrop(p.HDrop()) })

	// Ctrl+V 粘贴文件:
	// 1) 焦点在主窗口(点击窗口空白)时, 主窗口收到 WM_KEYDOWN
	a.wnd.On().WmKeyDown(func(p ui.WmKey) {
		if p.Raw.WParam.LoWord() == uint16(co.VK_V) && ctrlDown() {
			a.onPaste()
		}
	})
	// 2) 焦点在列表/按钮上时, 用 OnSubclass 拦截 WM_KEYDOWN (需在窗口创建前注册,
	//    windigo 创建控件窗口时会自动 SetWindowSubclass)
	a.hookPasteKey(a.lv)
	a.hookPasteKey(a.ckShow)
	a.hookPasteKey(btnAdd)
	a.hookPasteKey(btnRm)
	a.hookPasteKey(btnClr)
	a.hookPasteKey(btnPaste)
	a.hookPasteKey(btnBrowse)
	a.hookPasteKey(a.btnAuto)
	a.hookPasteKey(a.btnEnc)
	a.hookPasteKey(a.btnDec)

	_, _ = win.CoInitializeEx(co.COINIT_APARTMENTTHREADED | co.COINIT_DISABLE_OLE1DDE)
	a.wnd.RunAsMain()
}

func (a *appUI) pollProgress() {
	st := giStatus.Load()
	switch st {
	case stRun:
		pct := int(giProgress.Load())
		if pct < 0 {
			pct = 0
		} else if pct > 100 {
			pct = 100
		}
		a.pb.SetPos(pct)
		if nameV := giCurName.Load(); nameV != nil {
			name := nameV.(string)
			a.lblStat.SetTextAndResize(fmt.Sprintf("处理中: %s  %d%%", name, pct))
		}
	case stDoneOK:
		a.busy.Store(false)
		a.setRunBtnsEnabled(true)
		a.pb.SetPos(100)
		pct := int(giProgress.Load())
		giStatus.Store(stIdle)
		a.lblStat.SetTextAndResize(fmt.Sprintf("完成: 成功处理 %d 个文件", pct))
		_, _ = a.wnd.Hwnd().MessageBox(
			fmt.Sprintf("成功处理 %d 个文件", pct),
			"完成", co.MB_OK|co.MB_ICONINFORMATION)
	case stDoneErr:
		a.busy.Store(false)
		a.setRunBtnsEnabled(true)
		a.pb.SetPos(0)
		errMsg := ""
		if v := giErrMsg.Load(); v != nil {
			errMsg = v.(string)
		}
		pct := int(giProgress.Load())
		giStatus.Store(stIdle)
		a.lblStat.SetTextAndResize(errMsg)
		_, _ = a.wnd.Hwnd().MessageBox(
			fmt.Sprintf("成功 %d 个, 失败 1 个\n\n%s", pct, errMsg),
			"未完成", co.MB_OK|co.MB_ICONERROR)
	}
}

func (a *appUI) onAdd() {
	files := pickFiles(a.wnd.Hwnd())
	if len(files) > 0 {
		a.ingest(files, "添加")
	}
}

// 把一批文件加入列表并刷新界面 (verb: 添加/拖入/粘贴)
func (a *appUI) ingest(files []string, verb string) {
	filtered := make([]string, 0, len(files))
	for _, p := range files {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			filtered = append(filtered, p)
		}
	}
	if len(filtered) == 0 {
		return
	}
	n := a.fl.add(filtered)
	if n > 0 {
		a.refreshList()
		a.ensureDefaultOutdir(filtered[0])
		a.lblStat.SetTextAndResize(fmt.Sprintf("已%s %d 个文件", verb, n))
	}
}

// 传统拖放注册: 资源管理器拖文件到窗口 -> 触发 WM_DROPFILES (无需 OLE 初始化)
func dragAcceptFiles(hwnd win.HWND, accept bool) {
	proc := syscall.NewLazyDLL("shell32.dll").NewProc("DragAcceptFiles")
	b := uintptr(0)
	if accept {
		b = 1
	}
	_, _, _ = proc.Call(uintptr(hwnd), b)
}

// 拖入文件到窗口 (WM_DROPFILES)
func (a *appUI) onDrop(hDrop win.HDROP) {
	paths, err := hDrop.DragQueryFile()
	defer hDrop.DragFinish() // WM_DROPFILES 来源必须释放
	if err != nil || len(paths) == 0 {
		return
	}
	a.ingest(paths, "拖入")
}

// Ctrl+V / 粘贴文件按钮: 从剪贴板读取资源管理器复制的文件
func (a *appUI) onPaste() {
	files := readClipboardFiles()
	if len(files) == 0 {
		a.lblStat.SetTextAndResize("剪贴板中没有复制的文件 (请先在资源管理器 Ctrl+C 复制)")
		return
	}
	a.ingest(files, "粘贴")
}

// 读取剪贴板中资源管理器复制的文件路径 (CF_HDROP)
func readClipboardFiles() []string {
	hClip, err := win.OpenClipboard(win.HWND(0))
	if err != nil {
		return nil
	}
	defer hClip.CloseClipboard()
	avail, err := hClip.IsClipboardFormatAvailable(co.CF_HDROP)
	if err != nil || !avail {
		return nil
	}
	data, err := hClip.GetClipboardData(co.CF_HDROP)
	if err != nil || len(data) < 20 {
		return nil
	}
	return parseHDROP(data)
}

// 解析剪贴板 CF_HDROP: 前 20 字节 DROPFILES 结构 + 之后的文件列表
func parseHDROP(data []byte) []string {
	pFiles := binary.LittleEndian.Uint32(data[0:4])
	fWide := binary.LittleEndian.Uint32(data[16:20]) != 0 // fWide 在偏移 16
	if int(pFiles) > len(data) {
		return nil
	}
	blob := data[pFiles:]
	var out []string
	if fWide { // UTF-16LE, 双 null 结尾
		n := len(blob) / 2
		u := make([]uint16, n)
		for i := 0; i < n; i++ {
			u[i] = binary.LittleEndian.Uint16(blob[i*2:])
		}
		start := 0
		for i := 0; i <= len(u); i++ {
			if i == len(u) || u[i] == 0 {
				if i == start {
					break // 空段 = 双 null = 列表结束
				}
				out = append(out, string(utf16.Decode(u[start:i])))
				start = i + 1
			}
		}
	} else { // ANSI 兜底 (极少见)
		cur := make([]byte, 0)
		for _, b := range blob {
			if b == 0 {
				if len(cur) == 0 {
					break
				}
				out = append(out, string(cur))
				cur = cur[:0]
			} else {
				cur = append(cur, b)
			}
		}
	}
	return out
}

// 输出目录为空时, 自动填第一个文件的所在目录 (默认=同目录)
func (a *appUI) ensureDefaultOutdir(firstFile string) {
	if strings.TrimSpace(a.edtOut.Text()) == "" {
		a.edtOut.SetText(filepath.Dir(firstFile))
	}
}

func (a *appUI) refreshList() {
	a.lv.DeleteAllItems()
	for _, p := range a.fl.snapshot() {
		a.lv.AddItem(p)
	}
	// 强制 ListView 重绘 (确保部分场景下文字即时显示)
	a.lv.Hwnd().InvalidateRect(nil, true)
}

func (a *appUI) onRemove() {
	idxs := a.lv.SelectedItems()
	if len(idxs) == 0 {
		return
	}
	plainIdxs := make([]int, 0, len(idxs))
	for _, it := range idxs {
		plainIdxs = append(plainIdxs, it.Index())
	}
	a.fl.removeSelected(plainIdxs)
	a.refreshList()
	a.lblStat.SetTextAndResize("就绪")
}

func (a *appUI) onClear() {
	a.fl.clear()
	a.refreshList()
	a.lblStat.SetTextAndResize("就绪")
}

func (a *appUI) onBrowse() {
	dir := pickFolder(a.wnd.Hwnd())
	if dir != "" {
		a.edtOut.SetText(dir)
	}
}

func (a *appUI) onStart(mode int) {
	if a.busy.Load() {
		return
	}
	files := a.fl.snapshot()
	if len(files) == 0 {
		_, _ = a.wnd.Hwnd().MessageBox("请先添加要处理的文件", "提示",
			co.MB_OK|co.MB_ICONINFORMATION)
		return
	}
	ops := buildOps(files, mode)
	needEnc := false
	for _, op := range ops {
		if op {
			needEnc = true
			break
		}
	}
	pw := a.edtPw1.Text()
	if pw == "" {
		_, _ = a.wnd.Hwnd().MessageBox("请输入密码", "提示",
			co.MB_OK|co.MB_ICONINFORMATION)
		return
	}
	if needEnc { // 存在加密操作时才要求确认密码 (纯解密跳过, 更快捷)
		if a.edtPw2.Text() == "" {
			_, _ = a.wnd.Hwnd().MessageBox("请再次输入密码确认", "提示",
				co.MB_OK|co.MB_ICONINFORMATION)
			return
		}
		if pw != a.edtPw2.Text() {
			_, _ = a.wnd.Hwnd().MessageBox("两次输入的密码不一致", "提示",
				co.MB_OK|co.MB_ICONINFORMATION)
			return
		}
	}

	outdir := strings.TrimSpace(a.edtOut.Text())
	if outdir != "" {
		if st, err := os.Stat(outdir); err != nil || !st.IsDir() {
			_, _ = a.wnd.Hwnd().MessageBox("输出目录无效", "提示",
				co.MB_OK|co.MB_ICONWARNING)
			return
		}
	}

	a.busy.Store(true)
	a.setRunBtnsEnabled(false)
	a.pb.SetPos(0)
	if mode == modeAuto {
		encN, decN := 0, 0
		for _, op := range ops {
			if op {
				encN++
			} else {
				decN++
			}
		}
		a.lblStat.SetTextAndResize(fmt.Sprintf("准备中... 自动识别: 加密 %d 个 / 解密 %d 个", encN, decN))
	} else {
		a.lblStat.SetTextAndResize("准备中...")
	}

	go a.worker(files, ops, pw, outdir)
}

// 按模式决定每个文件执行的操作: true=加密, false=解密
func (a *appUI) worker(files []string, ops []bool, password, outdir string) {
	outs := make([]string, len(files))
	conflict := 0
	for i, f := range files {
		outs[i] = defaultOutpath(f, ops[i], outdir)
		if _, err := os.Stat(outs[i]); err == nil {
			conflict++
		}
	}
	if conflict > 0 {
		btn, _ := a.wnd.Hwnd().MessageBox(
			fmt.Sprintf("%d 个输出文件已存在, 是否覆盖?", conflict),
			"文件已存在", co.MB_YESNO|co.MB_ICONQUESTION)
		if btn != co.ID_YES {
			giStatus.Store(stIdle)
			a.busy.Store(false)
			a.setRunBtnsEnabled(true)
			a.lblStat.SetTextAndResize("已取消")
			return
		}
	}

	giStatus.Store(stRun)
	giProgress.Store(0)
	giCurName.Store("")

	totalBytes := int64(0)
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			totalBytes += st.Size()
		}
	}
	if totalBytes == 0 {
		totalBytes = 1
	}

	okCount := int64(0)
	var firstErr string
	doneBefore := int64(0)

	for i, f := range files {
		base := doneBefore
		name := filepath.Base(f)
		if ops[i] {
			giCurName.Store("加密: " + name)
		} else {
			giCurName.Store("解密: " + name)
		}

		progress := func(done, total int64) {
			pct := int((base + done) * 100 / totalBytes)
			if pct > 100 {
				pct = 100
			}
			giProgress.Store(int64(pct))
		}

		var err error
		if ops[i] {
			err = encryptFile(f, outs[i], password, progress)
		} else {
			err = decryptFile(f, outs[i], password, progress)
		}
		if err != nil {
			if firstErr == "" {
				firstErr = fmt.Sprintf("%s: %v", name, err)
			}
		} else {
			okCount++
		}
		if st, err2 := os.Stat(f); err2 == nil {
			doneBefore += st.Size()
		}
	}

	giProgress.Store(okCount)
	if firstErr != "" {
		giErrMsg.Store(firstErr)
		giStatus.Store(stDoneErr)
	} else {
		giStatus.Store(stDoneOK)
	}
}

// 统一启停三个处理按钮 (智能处理/加密/解密)
func (a *appUI) setRunBtnsEnabled(en bool) {
	a.btnAuto.Hwnd().EnableWindow(en)
	a.btnEnc.Hwnd().EnableWindow(en)
	a.btnDec.Hwnd().EnableWindow(en)
}

func pickFiles(hwndOwner win.HWND) []string {
	rel := win.NewOleReleaser()
	defer rel.Release()
	var fod *win.IFileOpenDialog
	if err := win.CoCreateInstance(rel, &co.CLSID_FileOpenDialog, nil,
		co.CLSCTX_INPROC_SERVER, &fod); err != nil {
		return nil
	}
	_ = fod.SetTitle("选择要处理的文件")
	_ = fod.SetFileTypes([]win.COMDLG_FILTERSPEC{
		{Name: "所有文件", Spec: "*.*"},
	})
	_ = fod.SetOptions(co.FOS_FORCEFILESYSTEM | co.FOS_FILEMUSTEXIST | co.FOS_ALLOWMULTISELECT)
	if ok, _ := fod.Show(hwndOwner); !ok {
		return nil
	}
	results, err := fod.GetResults(rel)
	if err != nil || results == nil {
		return nil
	}
	cnt, _ := results.GetCount()
	out := make([]string, 0, cnt)
	for i := 0; i < cnt; i++ {
		item, _ := results.GetItemAt(rel, i)
		if item == nil {
			continue
		}
		path, _ := item.GetDisplayName(co.SIGDN_FILESYSPATH)
		if path != "" {
			out = append(out, path)
		}
	}
	return out
}

func pickFolder(hwndOwner win.HWND) string {
	rel := win.NewOleReleaser()
	defer rel.Release()
	var fod *win.IFileOpenDialog
	if err := win.CoCreateInstance(rel, &co.CLSID_FileOpenDialog, nil,
		co.CLSCTX_INPROC_SERVER, &fod); err != nil {
		return ""
	}
	_ = fod.SetTitle("选择输出目录")
	_ = fod.SetOptions(co.FOS_PICKFOLDERS | co.FOS_FORCEFILESYSTEM)
	if ok, _ := fod.Show(hwndOwner); !ok {
		return ""
	}
	result, err := fod.GetResult(rel)
	if err != nil || result == nil {
		return ""
	}
	path, _ := result.GetDisplayName(co.SIGDN_FILESYSPATH)
	return path
}

// ---------- Ctrl+V 粘贴文件 (键盘捕获) ----------

// 子类化挂钩 Ctrl+V: 焦点在列表/按钮等控件上时也能粘贴文件
type keyHooker interface {
	OnSubclass() *ui.WindowEvents
	Hwnd() win.HWND
}

func (a *appUI) hookPasteKey(c keyHooker) {
	c.OnSubclass().Wm(co.WM_KEYDOWN, func(p ui.Wm) uintptr {
		if p.WParam.LoWord() == uint16(co.VK_V) && ctrlDown() {
			a.onPaste()
			return 0 // 已消费, 不触发控件默认行为
		}
		// 放行其它按键 (方向键/空格/回车等交给控件默认处理)
		return c.Hwnd().DefSubclassProc(p.Msg, p.WParam, p.LParam)
	})
}

// Ctrl 是否按下 (GetAsyncKeyState 与线程无关, 子类回调里同样可靠)
func ctrlDown() bool {
	return win.GetAsyncKeyState(co.VK_CONTROL)&0x8000 != 0
}
