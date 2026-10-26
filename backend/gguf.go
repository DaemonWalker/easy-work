package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type ggufReader struct{ r io.ReadSeeker }

func (g *ggufReader) u32() (uint32, error) {
	var v uint32
	err := binary.Read(g.r, binary.LittleEndian, &v)
	return v, err
}
func (g *ggufReader) u64() (uint64, error) {
	var v uint64
	err := binary.Read(g.r, binary.LittleEndian, &v)
	return v, err
}
func (g *ggufReader) str() (string, error) {
	n, err := g.u64()
	if err != nil {
		return "", err
	}
	if n > 16<<20 {
		return "", fmt.Errorf("GGUF 字符串过长")
	}
	b := make([]byte, n)
	_, err = io.ReadFull(g.r, b)
	return string(b), err
}
func (g *ggufReader) skip(t uint32) (any, error) {
	switch t {
	case 0, 1, 2, 3, 4, 5, 6:
		_, err := g.r.Seek(1, io.SeekCurrent)
		return nil, err
	case 7, 8, 9:
		_, err := g.r.Seek(2, io.SeekCurrent)
		return nil, err
	case 10, 11, 12:
		_, err := g.r.Seek(4, io.SeekCurrent)
		return nil, err
	case 13:
		_, err := g.r.Seek(8, io.SeekCurrent)
		return nil, err
	case 9 + 100:
		return nil, fmt.Errorf("bad")
	default:
		return nil, fmt.Errorf("未知 GGUF 类型 %d", t)
	}
}
func (g *ggufReader) value(t uint32) (any, error) {
	switch t {
	case 0:
		var v uint8
		return v, binary.Read(g.r, binary.LittleEndian, &v)
	case 1:
		var v int8
		return v, binary.Read(g.r, binary.LittleEndian, &v)
	case 2:
		var v uint16
		return v, binary.Read(g.r, binary.LittleEndian, &v)
	case 3:
		var v int16
		return v, binary.Read(g.r, binary.LittleEndian, &v)
	case 4:
		var v uint32
		return v, binary.Read(g.r, binary.LittleEndian, &v)
	case 5:
		var v int32
		return v, binary.Read(g.r, binary.LittleEndian, &v)
	case 6:
		var v float32
		return v, binary.Read(g.r, binary.LittleEndian, &v)
	case 7:
		var v bool
		return v, binary.Read(g.r, binary.LittleEndian, &v)
	case 8:
		return g.str()
	case 10:
		var v uint64
		return v, binary.Read(g.r, binary.LittleEndian, &v)
	case 11:
		var v int64
		return v, binary.Read(g.r, binary.LittleEndian, &v)
	case 12:
		var v float64
		return v, binary.Read(g.r, binary.LittleEndian, &v)
	case 9:
		et, err := g.u32()
		if err != nil {
			return nil, err
		}
		n, err := g.u64()
		if err != nil {
			return nil, err
		}
		for i := uint64(0); i < n; i++ {
			if _, err = g.value(et); err != nil {
				return nil, err
			}
		}
		return n, nil
	default:
		return nil, fmt.Errorf("未知 GGUF 类型 %d", t)
	}
}
func readGGUF(path string) (ModelInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return ModelInfo{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ModelInfo{}, err
	}
	g := &ggufReader{r: f}
	magic := make([]byte, 4)
	if _, err = io.ReadFull(f, magic); err != nil || string(magic) != "GGUF" {
		return ModelInfo{}, fmt.Errorf("不是有效 GGUF 文件")
	}
	if _, err = g.u32(); err != nil {
		return ModelInfo{}, err
	}
	if _, err = g.u64(); err != nil {
		return ModelInfo{}, err
	}
	count, err := g.u64()
	if err != nil {
		return ModelInfo{}, err
	}
	values := map[string]any{}
	for i := uint64(0); i < count; i++ {
		key, e := g.str()
		if e != nil {
			return ModelInfo{}, e
		}
		t, e := g.u32()
		if e != nil {
			return ModelInfo{}, e
		}
		v, e := g.value(t)
		if e != nil {
			return ModelInfo{}, fmt.Errorf("%s: %w", key, e)
		}
		values[key] = v
	}
	arch, _ := values["general.architecture"].(string)
	kind := "chat"
	if strings.Contains(filepath.ToSlash(path), "/vl/") {
		kind = "vl"
	}
	info := ModelInfo{Kind: kind, Name: filepath.Base(path), ID: filepath.Base(path), Path: path, Size: st.Size(), MaxContext: int(numberValue(values[arch+".context_length"])), Meta: map[string]any{}}
	info.Meta["n_params"] = numberValue(values["general.parameter_count"])
	info.Meta["n_embd"] = numberValue(values[arch+".embedding_length"])
	info.Meta["n_vocab"] = numberValue(values["tokenizer.ggml.tokens"])
	fileTypes := map[int]string{0: "F32", 1: "F16", 2: "Q4_0", 3: "Q4_1", 7: "Q8_0", 8: "Q5_0", 9: "Q5_1", 10: "Q2_K", 11: "Q3_K_S", 12: "Q3_K_M", 13: "Q3_K_L", 14: "Q4_K_S", 15: "Q4_K_M", 16: "Q5_K_S", 17: "Q5_K_M", 18: "Q6_K"}
	ftype := int(numberValue(values["general.file_type"]))
	info.Meta["ftype"] = fileTypes[ftype]
	if info.Meta["ftype"] == "" {
		info.Meta["ftype"] = fmt.Sprintf("file_type=%d", ftype)
	}
	info.Meta["architecture"] = arch
	return info, nil
}
func numberValue(v any) float64 {
	switch n := v.(type) {
	case uint8:
		return float64(n)
	case int8:
		return float64(n)
	case uint16:
		return float64(n)
	case int16:
		return float64(n)
	case uint32:
		return float64(n)
	case int32:
		return float64(n)
	case uint64:
		return float64(n)
	case int64:
		return float64(n)
	case float32:
		return float64(n)
	case float64:
		return n
	}
	return 0
}
func scanModels(root string) ([]ModelInfo, error) {
	files, err := filepath.Glob(filepath.Join(root, "gguf", "chat", "*.gguf"))
	if err != nil {
		return nil, err
	}
	out := []ModelInfo{}
	for _, p := range files {
		if strings.HasSuffix(strings.ToLower(p), "-mmproj.gguf") {
			continue
		}
		m, err := readGGUF(p)
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}
