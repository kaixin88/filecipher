# FileCipher v2.0 — Go 重写版

## 新增便捷功能 (v2.1)

- **拖拽文件**: 资源管理器选中文件直接拖到窗口 → 自动加入列表
- **默认密码 `123`**: 密码框预填, 不改可直接加密
- **默认输出目录 = 同目录**: 拖入/添加文件后, 输出目录自动填入首个文件所在目录
- **双击零黑框**: 两个 exe 启动 GUI 时都会自动销毁黑色控制台窗口 (FreeConsole)
- **列表显示 bug 修复**: 修复了"添加文件后列表里不显示文字"——`AddCol` 必须在 `OptsListView().Column()` 中声明 (windigo 延迟创建机制下, 控件 hWnd=0 时调用 `AddCol` 静默失败, 列从未插入)
- 模式切换智能提示: 切到解密时禁用确认框并清空; 切回加密时自动恢复 `123`

## v2.2 改进

- **解密容错增强** — `.fcp` 文件末尾若被某些工具/聊天传输追加少量垃圾字节 (如 11 个空格), 解密器会**自动忽略**正常解出 (不再误报"文件已损坏"); 截断或被篡改的真正损坏文件仍会正确报错
- **加密/解密分立双按钮** — 去掉顶部"加密/解密"单选 RadioGroup, 改为两个独立按钮 "开 始 加 密" 和 "开 始 解 密", 点击哪个执行哪个, 更快捷
- 解密自动跳过确认密码 (默认 123, 只用"密码"框校验); 加密仍需两次密码一致
- 已知污染场景: 文件经企业微信等 IM 发送后保存, 尾部可能被追加垃圾字节 (实测 11 个空格), 解密器自动忽略; 若文件中间被篡改或截断则仍会明确报错 (GCM 认证保证不静默解出错误数据)

## v2.3 改进

- **智能处理按钮 (自动识别加密/解密)** — 列表里可同时含普通文件与 `.fcp` 文件, 点 `智 能 处 理` 一键处理: `.fcp` 自动解密, 其余自动加密; 状态栏会显示"自动识别: 加密 N 个 / 解密 M 个"。原"开始加密""开始解密"按钮保留为强制模式入口
- **Ctrl+V 粘贴文件** — 在资源管理器 Ctrl+C 复制文件后, 切到 FileCipher 直接 Ctrl+V 加入列表; 焦点无论在窗口/列表/按钮均生效 (用 windigo OnSubclass 子类化控件拦截 WM_KEYDOWN, 不影响密码框内文本粘贴)。右侧栏新增 `粘贴文件` 按钮作为兜底入口

## macOS 版 (v2.3+)

Windows 版 GUI 用 windigo (Win32 API), **只支持 Windows**。macOS 版改用跨平台框架 **Fyne v2** 重写了界面层 (`gui_mac.go`, `//go:build darwin`), 与 Windows 版**共享同一份纯 Go 加密核心 `core.go`**:

- **`.fcp` 文件 Mac / Windows 完全互通** — 任何一端加密的文件, 另一端可直接解密
- 功能对齐: 文件列表 / Finder 拖入多文件 / 添加文件 / 智能处理 (自动识别) / 开始加密 / 开始解密 / 默认密码 123 / 输出目录 (默认同源目录) / 进度条 / 完成弹窗
- 源码托管: `https://github.com/kaixin88/filecipher` (公开)
- **自动打包**: 仓库内 `.github/workflows/build-mac.yml` 在 GitHub Actions 的 macOS 云构建机上编译 `arm64 + amd64`, `lipo` 合成 universal 二进制, 组装 `FileCipher.app` (ad-hoc 签名) 并上传 zip 产物

下载与安装:

1. 打开仓库 `https://github.com/kaixin88/filecipher` → **Actions** 页 → 最新一次成功的 **Build macOS** 运行 → 底部 **Artifacts** → 下载 `FileCipher-mac.zip`
2. 解压后把 `FileCipher.app` 拖入「应用程序」(或直接双击运行)
3. 因未做 Apple 开发者签名, 首次打开若被 Gatekeeper 拦截: 在 Finder 中 **右键 FileCipher.app → 打开** (或终端执行 `xattr -dr com.apple.quarantine /Applications/FileCipher.app` 后即可正常双击)

macOS 命令行版也可在任何平台交叉编译 (无需 Fyne/CGO):

```bash
GOOS=darwin GOARCH=arm64 go build -ldflags "-s -w" -o FileCipher-mac .
```

## 解决什么问题

Python 版 (`PyInstaller`) 的两个痛点:

1. **启动黑屏 5-10 秒** — 解压 Python 运行时,慢
2. **批量添加 2 个文件就卡 UI** — tkinter 单线程,文件 IO 阻塞主线程

Go 版直接编译成原生 Win32 GUI 程序,启动毫秒级,后台 goroutine 处理 + 主线程 timer 轮询,绝不卡 UI。

## 文件清单

```
FileCipherGo/
├── dist/
│   └── FileCipher.exe           # GUI 版 (2.4MB, 无控制台, 双击即用)
├── FileCipher-cli.exe           # CLI 版 (2.4MB, 命令行用; 双击也会进 GUI 且无黑框)
├── core.go                      # AES-256-GCM + scrypt, 格式兼容 v1.1
├── gui.go                       # windigo GUI (纯 Go, Go 1.16+)
├── main.go                      # 入口 (无参 → GUI, encrypt/decrypt → CLI)
├── go.mod                       # 依赖 windigo v0.2.6 + x/crypto v0.55.0
└── test/                        # 回归测试 + 启动截图
```

## 使用方法

### GUI 模式

直接双击 `dist/FileCipher.exe`,或在 cmd 里运行:

```
FileCipher.exe                  # 启动 GUI
FileCipher.exe -g               # 强制 GUI
```

GUI 操作:

1. 选择 **加 密** 或 **解 密**
2. 点 **添加文件...** 选择一个或多个文件(支持多选)
3. 选择 **输出目录**(留空 = 原目录)
4. 输入 **密码** 与 **确认**(解密无需确认),勾选 **显示** 可看清密码
6. 点 **开 始 加 密** 或 **开 始 解 密**
7. 底部状态栏显示进度,完成后弹窗提示

### CLI 模式

```
FileCipher.exe encrypt <文件> [-o 输出] [-p 密码] [-f]
FileCipher.exe decrypt <文件> [-o 输出] [-p 密码] [-f]
FileCipher.exe -i              # 命令行交互模式
FileCipher.exe --help          # 帮助
```

`[-f]` 表示强制覆盖已存在的输出文件。

### 文件格式

`.fcp` 文件格式 = `FCP1` 魔数 + version 字节 + 16B 盐 + 12B base nonce + 8B 总大小 + 分块密文。

**与 Python v1.1 完全二进制兼容**: 用 Python v1.1 加密的 `.fcp` 文件,Go v2.0 能解密;Go 加密的也能用 Python v1.1 解密。

## 关键改进 (vs Python 版)

| 指标           | Python v1.1 | Go v2.0         |
|----------------|-------------|-----------------|
| 启动耗时       | 5-10 秒     | **25-47 ms**    |
| GUI exe 体积   | ~15 MB      | **2.4 MB**      |
| 添加多文件     | 卡 UI       | **不卡(后台 goroutine)** |
| 加密吞吐       | ~3 MB/s     | ~4.8 MB/s       |
| 依赖安装       | 需 Python+tkinter+Crypto | **零依赖,纯 Go 静态链接** |

## 技术栈

* Go 1.27 + `windigo` (纯 Go Win32 syscall 绑定,无 CGO)
* `golang.org/x/crypto` 提供 AES-256-GCM + scrypt KDF(N=2^15, r=8, p=1)
* 后台 `worker()` goroutine + 主线程 `WmTimer(100ms)` 轮询 `giProgress` / `giStatus` atomic 变量,UI 永不阻塞

## 编译

```bash
export PATH="/c/Users/zlwdr/.workbuddy/binaries/go/go/bin:$PATH"
export GOPATH="C:/Users/zlwdr/.workbuddy/binaries/go/gopath"
export GOCACHE="C:/Users/zlwdr/.workbuddy/binaries/go/gocache"
export GOPROXY="https://goproxy.cn,direct"
export GOTOOLCHAIN=local

# GUI 版 (windowsgui 子系统, 双击无黑框)
go build -ldflags "-s -w -H windowsgui" -o dist/FileCipher.exe .

# CLI 版 (console 子系统, 命令行输出正常; runGUI 内 FreeConsole 双击时自动消黑框)
go build -ldflags "-s -w" -o FileCipher-cli.exe .
```

> 黑框排查记录: 若双击 exe 出现黑色控制台窗口, 说明该 exe 编译成了
> console 子系统。GUI 版必须加 `-H windowsgui`; 代码中 `runGUI()` 开头
> 调用 `FreeConsole()` 兜底, 即使 CLI 版被双击, 黑框也会立即销毁。

## 回归测试

```bash
cd FileCipherGo/test
python regression.py        # 12 项加解密测试 (小/大/空/错误密码)
python launch_bench.py      # 5 次启动耗时基准
```