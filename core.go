package main

// 加密核心: 与 Python 版 v1.1 格式完全兼容
// 算法: AES-256-GCM (encrypt-then-authenticate)
// 密钥派生: scrypt (内存困难型, 抗 GPU 暴力破解)
// 文件格式: FCP1 | version(1) | salt(16) | base_nonce(12) | total(8BE)
//          然后逐块: 块长(4BE) + (nonce+ciphertext+tag)

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/scrypt"
)

const (
	MAGIC       = "FCP1"
	VERSION     = 1
	SALT_LEN    = 16
	NONCE_LEN   = 12
	TAG_LEN     = 16
	CHUNK_SIZE  = 1 << 20 // 1 MiB 分块
	SCRYPT_N    = 1 << 15 // 32MB 内存
	SCRYPT_R    = 8
	SCRYPT_P    = 1
	HEADER_SIZE = 4 + 1 + SALT_LEN + NONCE_LEN + 8
)

const VERSION_INFO = "FileCipher v2.3 (Go / AES-256-GCM / scrypt)"

// ---------- 密钥与加密原语 ----------

func deriveKey(password string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(password), salt, SCRYPT_N, SCRYPT_R, SCRYPT_P, 32)
}

// blockNonce 每块唯一 nonce: 4 字节随机前缀 + 8 字节大端块号
func blockNonce(base []byte, idx uint64) []byte {
	n := make([]byte, NONCE_LEN)
	copy(n, base[:4])
	binary.BigEndian.PutUint64(n[4:], idx)
	return n
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// ---------- 默认输出路径 ----------

func defaultOutpath(src string, isEncrypt bool, outdir string) string {
	var name string
	if isEncrypt {
		name = src + ".fcp"
	} else {
		if strings.HasSuffix(strings.ToLower(src), ".fcp") {
			name = src[:len(src)-4]
		} else {
			name = src + ".out"
		}
	}
	if outdir != "" {
		name = filepath.Join(outdir, filepath.Base(name))
	}
	return name
}

// ---------- 加密 ----------

func encryptFile(src, dst, password string, progress func(done, total int64)) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("无法读取文件: %v", err)
	}
	total := info.Size()

	salt := make([]byte, SALT_LEN)
	baseNonce := make([]byte, NONCE_LEN)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return err
	}
	if _, err := io.ReadFull(rand.Reader, baseNonce); err != nil {
		return err
	}
	key, err := deriveKey(password, salt)
	if err != nil {
		return err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return err
	}

	fin, err := os.Open(src)
	if err != nil {
		return err
	}
	defer fin.Close()

	fout, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer fout.Close()

	header := make([]byte, HEADER_SIZE)
	copy(header[0:4], MAGIC)
	header[4] = VERSION
	copy(header[5:5+SALT_LEN], salt)
	copy(header[5+SALT_LEN:5+SALT_LEN+NONCE_LEN], baseNonce)
	binary.BigEndian.PutUint64(header[5+SALT_LEN+NONCE_LEN:], uint64(total))
	if _, err := fout.Write(header); err != nil {
		return err
	}

	nChunks := uint64(1)
	if total > 0 {
		nChunks = uint64((total + CHUNK_SIZE - 1) / CHUNK_SIZE)
	}

	buf := make([]byte, CHUNK_SIZE)
	var idx uint64
	var done int64
	lenBytes := make([]byte, 4)
	for {
		n, err := fin.Read(buf)
		if n > 0 {
			aad := make([]byte, 16)
			binary.BigEndian.PutUint64(aad[0:8], idx)
			binary.BigEndian.PutUint64(aad[8:16], nChunks)
			ct := gcm.Seal(nil, blockNonce(baseNonce, idx), buf[:n], aad)
			binary.BigEndian.PutUint32(lenBytes, uint32(len(ct)))
			if _, err := fout.Write(lenBytes); err != nil {
				return err
			}
			if _, err := fout.Write(ct); err != nil {
				return err
			}
			idx++
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if total == 0 { // 空文件写入一块认证数据
		aad := make([]byte, 16)
		binary.BigEndian.PutUint64(aad[0:8], 0)
		binary.BigEndian.PutUint64(aad[8:16], 1)
		ct := gcm.Seal(nil, blockNonce(baseNonce, 0), nil, aad)
		binary.BigEndian.PutUint32(lenBytes, uint32(len(ct)))
		if _, err := fout.Write(lenBytes); err != nil {
			return err
		}
		if _, err := fout.Write(ct); err != nil {
			return err
		}
	}
	return nil
}

// ---------- 解密 ----------

func decryptFile(src, dst, password string, progress func(done, total int64)) error {
	fin, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("无法打开文件: %v", err)
	}
	defer fin.Close()

	head := make([]byte, HEADER_SIZE)
	if _, err := io.ReadFull(fin, head); err != nil {
		return errors.New("不是有效的 FileCipher 加密文件")
	}
	if string(head[0:4]) != MAGIC {
		return errors.New("不是有效的 FileCipher 加密文件")
	}
	if head[4] != VERSION {
		return fmt.Errorf("不支持的版本: %d", head[4])
	}
	salt := head[5 : 5+SALT_LEN]
	baseNonce := head[5+SALT_LEN : 5+SALT_LEN+NONCE_LEN]
	total := int64(binary.BigEndian.Uint64(head[5+SALT_LEN+NONCE_LEN:]))

	key, err := deriveKey(password, salt)
	if err != nil {
		return err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return err
	}

	nChunks := uint64(1)
	if total > 0 {
		nChunks = uint64((total + CHUNK_SIZE - 1) / CHUNK_SIZE)
	}

	fout, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer fout.Close()

	var idx uint64
	var done int64
	sizeBuf := make([]byte, 4)
	for {
		if done >= total {
			// 全部明文已解出: 正常结束。容忍文件尾部被追加的多余字节
			// (否则多余字节会被误读成"块长字段", 报"文件已损坏"误伤)
			break
		}
		if _, err := io.ReadFull(fin, sizeBuf); err != nil {
			break // 数据不足: 交由下方 done!=total 统一判断
		}
		size := binary.BigEndian.Uint32(sizeBuf)
		ct := make([]byte, size)
		if _, err := io.ReadFull(fin, ct); err != nil {
			return cleanupErr(fout, dst, errors.New("文件已损坏(数据不完整)"))
		}
		aad := make([]byte, 16)
		binary.BigEndian.PutUint64(aad[0:8], idx)
		binary.BigEndian.PutUint64(aad[8:16], nChunks)
		plain, err := gcm.Open(nil, blockNonce(baseNonce, idx), ct, aad)
		if err != nil {
			return cleanupErr(fout, dst, errors.New("密码错误或文件已损坏, 无法解密"))
		}
		if _, err := fout.Write(plain); err != nil {
			return err
		}
		idx++
		done += int64(len(plain))
		if progress != nil {
			progress(done, total)
		}
	}
	if done != total {
		return cleanupErr(fout, dst, errors.New("解密数据不完整: 头部记录 "+fmt.Sprint(total)+" 字节, 实际解出 "+fmt.Sprint(done)+" 字节, 文件可能被截断"))
	}
	return nil
}

func cleanupErr(f *os.File, dst string, err error) error {
	f.Close()
	os.Remove(dst)
	return err
}
