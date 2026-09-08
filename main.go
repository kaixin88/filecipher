package main

// 入口: 无参数 -> GUI; encrypt/decrypt -> 命令行; -i -> 交互; -h -> 帮助

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func usage() {
	fmt.Println(VERSION_INFO)
	fmt.Println("用法:")
	fmt.Println("  FileCipher.exe                        # 启动图形界面(双击即用)")
	fmt.Println("  FileCipher.exe -g | --gui             # 强制图形界面")
	fmt.Println("  FileCipher.exe -i | --interactive     # 命令行交互模式")
	fmt.Println("  FileCipher.exe encrypt <文件> [-o 输出] [-p 密码] [-f]   # 加密")
	fmt.Println("  FileCipher.exe decrypt <文件> [-o 输出] [-p 密码] [-f]   # 解密")
}

func readPassword(confirm bool) (string, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("  输入密码: ")
	p1, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	p1 = strings.TrimRight(p1, "\r\n")
	if confirm {
		fmt.Print("  再次输入: ")
		p2, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		p2 = strings.TrimRight(p2, "\r\n")
		if p1 != p2 {
			return "", fmt.Errorf("两次密码不一致")
		}
	}
	if p1 == "" {
		return "", fmt.Errorf("密码不能为空")
	}
	return p1, nil
}

func interactive() error {
	fmt.Println("======================================================")
	fmt.Println("  " + VERSION_INFO)
	fmt.Println("  单文件免安装 | 支持任意大小文件 | 格式兼容 v1.1")
	fmt.Println("======================================================")
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("\n请选择: [1] 加密文件  [2] 解密文件  [q] 退出 > ")
		line, _ := reader.ReadString('\n')
		choice := strings.TrimSpace(strings.ToLower(line))
		if choice == "q" || choice == "quit" || choice == "exit" {
			return nil
		}
		if choice != "1" && choice != "2" {
			continue
		}
		isEnc := choice == "1"

		fmt.Print("输入文件路径 (可直接拖拽文件到本窗口): ")
		src, _ := reader.ReadString('\n')
		src = strings.Trim(strings.TrimSpace(src), `"`)
		if _, err := os.Stat(src); err != nil {
			fmt.Printf("[错误] 文件不存在: %s\n", src)
			continue
		}

		def := defaultOutpath(src, isEnc, "")
		fmt.Printf("输出文件 [回车默认 %s]: ", def)
		dst, _ := reader.ReadString('\n')
		dst = strings.Trim(strings.TrimSpace(dst), `"`)
		if dst == "" {
			dst = def
		}

		pw, err := readPassword(isEnc)
		if err != nil {
			fmt.Printf("[错误] %v\n", err)
			continue
		}

		var perr error
		if isEnc {
			perr = encryptFile(src, dst, pw, nil)
		} else {
			perr = decryptFile(src, dst, pw, nil)
		}
		if perr != nil {
			fmt.Printf("[错误] %v\n", perr)
		} else {
			fmt.Printf("  完成: %s\n", dst)
		}
	}
}

func cliMode(argv []string) error {
	isEnc := false
	switch argv[0] {
	case "encrypt", "e":
		isEnc = true
	case "decrypt", "d":
		isEnc = false
	default:
		usage()
		return nil
	}

	var src, dst, password string
	force := false
	rest := argv[1:]
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch a {
		case "-o", "--output":
			if i+1 >= len(rest) {
				return fmt.Errorf("-o 缺少参数")
			}
			i++
			dst = rest[i]
		case "-p", "--password":
			if i+1 >= len(rest) {
				return fmt.Errorf("-p 缺少参数")
			}
			i++
			password = rest[i]
		case "-f", "--force":
			force = true
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("未知参数: %s", a)
			}
			if src == "" {
				src = a
			} else {
				return fmt.Errorf("多余参数: %s", a)
			}
		}
	}
	if src == "" {
		return fmt.Errorf("请提供有效的输入文件路径")
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("文件不存在: %s", src)
	}
	if dst == "" {
		dst = defaultOutpath(src, isEnc, "")
	}
	if password == "" {
		pw, err := readPassword(isEnc)
		if err != nil {
			return err
		}
		password = pw
	}
	if !force {
		if _, err := os.Stat(dst); err == nil {
			return fmt.Errorf("输出文件已存在: %s (加 -f 可覆盖)", dst)
		}
	}

	if isEnc {
		return encryptFile(src, dst, password, nil)
	}
	return decryptFile(src, dst, password, nil)
}

func main() {
	argv := os.Args[1:]
	if len(argv) == 0 {
		runGUI()
		return
	}
	switch argv[0] {
	case "-g", "--gui", "gui":
		runGUI()
	case "-i", "--interactive", "i", "interactive":
		if err := interactive(); err != nil {
			fmt.Printf("[错误] %v\n", err)
			os.Exit(1)
		}
	case "help", "-h", "--help":
		usage()
	case "encrypt", "e", "decrypt", "d":
		if err := cliMode(argv); err != nil {
			fmt.Printf("[错误] %v\n", err)
			os.Exit(1)
		}
	default:
		usage()
	}
}
