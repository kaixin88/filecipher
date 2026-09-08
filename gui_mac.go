//go:build darwin

// FileCipher macOS 图形界面 (Fyne v2)。
// 与 Windows 版 (gui.go, windigo) 共享纯 Go 加密核心 core.go:
//   同一份 .fcp 文件可在 macOS / Windows 之间互相解密, 格式完全兼容。
//
// 功能对齐 Windows 版:
//   - 文件列表 (拖入 Finder 文件 / 添加文件按钮 / 逐行移除 / 清空)
//   - 智能处理 (自动识别: .fcp -> 解密, 其余 -> 加密)
//   - 开始加密 / 开始解密 双按钮
//   - 默认密码 123; 存在加密任务时校验两次密码一致
//   - 输出目录默认与源文件相同 (可指定)
//   - 进度条 / 状态栏 / 完成或错误弹窗

package main

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// macUI macOS 界面状态
type macUI struct {
	win fyne.Window

	files []string // 待处理文件 (已去重)

	scroll *container.Scroll
	box    *fyne.Container

	edtPw  *widget.Entry // 密码 (默认 123)
	edtPw2 *widget.Entry // 确认密码
	edtOut *widget.Entry // 输出目录 (空 = 与源文件同目录)

	btnSmart *widget.Button
	btnEnc   *widget.Button
	btnDec   *widget.Button

	pb  *widget.ProgressBar
	lbl *widget.Label

	busy atomic.Bool
}

// ---------- 列表操作 ----------

func (m *macUI) addFiles(paths []string) int {
	added := 0
	for _, p := range paths {
		dup := false
		for _, e := range m.files {
			if e == p {
				dup = true
				break
			}
		}
		if !dup {
			m.files = append(m.files, p)
			added++
		}
	}
	return added
}

func (m *macUI) removeFile(path string) {
	out := m.files[:0]
	for _, f := range m.files {
		if f != path {
			out = append(out, f)
		}
	}
	m.files = out
	m.refreshFiles()
}

func (m *macUI) clearFiles() {
	m.files = nil
	m.refreshFiles()
}

// 重建文件列表行 (每行: 移除按钮 + 文件路径)
func (m *macUI) refreshFiles() {
	m.box.Objects = nil
	for _, f := range m.files {
		p := f
		rm := widget.NewButton("✕", func() { m.removeFile(p) })
		lab := widget.NewLabel(f)
		row := container.NewBorder(nil, nil, nil, rm, lab)
		m.box.Add(row)
	}
	m.box.Refresh()
	m.scroll.Content = m.box
	m.scroll.Refresh()
	m.updateStatus(false)
}

func (m *macUI) updateStatus(running bool) {
	if running {
		return // 运行中由 worker 负责更新
	}
	if len(m.files) > 0 {
		m.lbl.SetText(fmt.Sprintf("已添加 %d 个文件 · 支持从 Finder 拖入多文件", len(m.files)))
	} else {
		m.lbl.SetText("就绪 - 拖入文件或点 [添加文件], 密码默认 123")
	}
}

// ---------- 文件入口 ----------

func (m *macUI) onAdd() {
	d := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil || r == nil {
			return
		}
		defer r.Close()
		m.ingestPaths([]string{r.URI().Path()})
	}, m.win)
	d.SetTitleText("选择要处理的文件")
	d.Show()
}

func (m *macUI) onDrop(_ fyne.Position, uris []fyne.URI) {
	paths := make([]string, 0, len(uris))
	for _, u := range uris {
		if u.Scheme() != "file" {
			continue
		}
		p := u.Path()
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			paths = append(paths, p)
		}
	}
	m.ingestPaths(paths)
}

// 统一入口: 去重加入 + 状态反馈
func (m *macUI) ingestPaths(paths []string) {
	if len(paths) == 0 {
		return
	}
	n := m.addFiles(paths)
	if n > 0 {
		m.refreshFiles()
	}
}

// ---------- 处理 ----------

func (m *macUI) start(mode int) {
	if m.busy.Load() {
		return
	}
	if len(m.files) == 0 {
		dialog.ShowInformation("提示", "请先添加要处理的文件", m.win)
		return
	}

	files := append([]string(nil), m.files...)
	ops := buildOps(files, mode)

	needEnc := false
	for _, op := range ops {
		if op {
			needEnc = true
			break
		}
	}

	pw := m.edtPw.Text
	if pw == "" {
		dialog.ShowInformation("提示", "请输入密码", m.win)
		return
	}
	if needEnc { // 存在加密操作才校验确认密码 (纯解密更快捷)
		if m.edtPw2.Text == "" {
			dialog.ShowInformation("提示", "请再次输入密码确认", m.win)
			return
		}
		if pw != m.edtPw2.Text {
			dialog.ShowInformation("提示", "两次输入的密码不一致", m.win)
			return
		}
	}

	outdir := strings.TrimSpace(m.edtOut.Text)
	if outdir != "" {
		if st, err := os.Stat(outdir); err != nil || !st.IsDir() {
			dialog.ShowInformation("提示", "输出目录无效", m.win)
			return
		}
	}

	// 预检输出冲突
	conflict := 0
	for i, f := range files {
		if _, err := os.Stat(defaultOutpath(f, ops[i], outdir)); err == nil {
			conflict++
		}
	}

	m.busy.Store(true)
	m.setRunEnabled(false)
	m.pb.SetValue(0)

	if mode == modeAuto {
		encN, decN := 0, 0
		for _, op := range ops {
			if op {
				encN++
			} else {
				decN++
			}
		}
		m.lbl.SetText(fmt.Sprintf("准备中... 自动识别: 加密 %d 个 / 解密 %d 个", encN, decN))
	} else {
		m.lbl.SetText("准备中...")
	}

	if conflict > 0 {
		dialog.ShowConfirm("文件已存在",
			fmt.Sprintf("%d 个输出文件已存在, 是否覆盖?", conflict),
			func(ok bool) {
				if ok {
					m.goWorker(files, ops, pw, outdir)
				} else {
					m.busy.Store(false)
					m.setRunEnabled(true)
					m.lbl.SetText("已取消")
				}
			}, m.win)
		return
	}
	m.goWorker(files, ops, pw, outdir)
}

func (m *macUI) setRunEnabled(en bool) {
	if en {
		m.btnSmart.Enable()
		m.btnEnc.Enable()
		m.btnDec.Enable()
	} else {
		m.btnSmart.Disable()
		m.btnEnc.Disable()
		m.btnDec.Disable()
	}
}

// 后台执行: 与 Windows 版 worker 逻辑一致
func (m *macUI) goWorker(files []string, ops []bool, password, outdir string) {
	go func() {
		totalBytes := int64(0)
		for _, f := range files {
			if st, err := os.Stat(f); err == nil {
				totalBytes += st.Size()
			}
		}
		if totalBytes == 0 {
			totalBytes = 1
		}

		okCount := 0
		var firstErr string
		doneBefore := int64(0)

		for i, f := range files {
			name := f
			base := doneBefore

			progress := func(done, total int64) {
				pct := int((base + done) * 100 / totalBytes)
				if pct > 100 {
					pct = 100
				}
				fyne.Do(func() {
					m.pb.SetValue(float64(pct) / 100.0)
					m.lbl.SetText(fmt.Sprintf("处理中... %d%%", pct))
				})
			}

			var err error
			if ops[i] {
				err = encryptFile(f, defaultOutpath(f, true, outdir), password, progress)
			} else {
				err = decryptFile(f, defaultOutpath(f, false, outdir), password, progress)
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

		fyne.Do(func() {
			m.busy.Store(false)
			m.setRunEnabled(true)
			if firstErr != "" {
				m.lbl.SetText("完成: 部分文件处理失败, 详情见弹窗")
				dialog.ShowError(fmt.Errorf("%s", firstErr), m.win)
			} else {
				m.pb.SetValue(1)
				m.lbl.SetText(fmt.Sprintf("完成: 成功处理 %d 个文件", okCount))
				dialog.ShowInformation("完成", fmt.Sprintf("成功处理 %d 个文件", okCount), m.win)
			}
		})
	}()
}

// ---------- 入口 ----------

func runGUI() {
	a := app.NewWithID("com.kaixin88.filecipher")
	w := a.NewWindow("FileCipher - 文件加密/解密")
	w.Resize(fyne.NewSize(800, 560))
	w.CenterOnScreen()

	m := &macUI{win: w}
	m.edtPw = widget.NewPasswordEntry()
	m.edtPw.SetText("123")
	m.edtPw2 = widget.NewPasswordEntry()
	m.edtOut = widget.NewEntry()
	m.edtOut.SetPlaceHolder("留空 = 与源文件同目录")
	btnBrowse := widget.NewButton("浏览...", func() {
		d := dialog.NewFolderOpen(func(lu fyne.ListableURI, err error) {
			if err != nil || lu == nil {
				return
			}
			m.edtOut.SetText(lu.Path())
		}, w)
		d.SetTitleText("选择输出目录")
		d.Show()
	})

	m.box = container.NewVBox()
	m.scroll = container.NewVScroll(m.box)

	btnAdd := widget.NewButton("添加文件", m.onAdd)
	btnClear := widget.NewButton("清空列表", m.clearFiles)

	m.btnSmart = widget.NewButton("智 能 处 理", func() { m.start(modeAuto) })
	m.btnEnc = widget.NewButton("开 始 加 密", func() { m.start(modeEnc) })
	m.btnDec = widget.NewButton("开 始 解 密", func() { m.start(modeDec) })

	m.pb = widget.NewProgressBar()
	m.lbl = widget.NewLabel("就绪 - 拖入文件或点 [添加文件], 密码默认 123")

	form := widget.NewForm()
	form.Append("密码", m.edtPw)
	form.Append("确认密码", m.edtPw2)
	form.Append("输出目录", container.NewBorder(nil, nil, nil, btnBrowse, m.edtOut))

	content := container.NewBorder(
		container.NewVBox(
			widget.NewLabel("支持从 Finder 拖入文件批量处理 · 解密自动跳过确认密码"),
			form,
			container.NewHBox(btnAdd, btnClear),
		),
		container.NewVBox(
			container.NewHBox(m.btnSmart, m.btnEnc, m.btnDec),
			m.pb,
			m.lbl,
		),
		nil, nil,
		m.scroll,
	)
	w.SetContent(content)

	w.SetOnDropped(m.onDrop)
	m.refreshFiles()
	w.ShowAndRun()
}
