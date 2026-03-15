package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Xsxdot/go-deploy/internal/core"
	"github.com/Xsxdot/go-deploy/pkg/maputil"
)

// rollbackData 回滚时删除生成的压缩包
type rollbackData struct {
	OutputPath string
}

// ArchivePlugin 打包插件，本地执行纯文件压缩（tar.gz/zip），并将路径注入全局上下文
type ArchivePlugin struct{}

// NewArchivePlugin 创建 archive 插件实例
func NewArchivePlugin() *ArchivePlugin {
	return &ArchivePlugin{}
}

// Name 实现 StepPlugin
func (p *ArchivePlugin) Name() string {
	return "archive"
}

// Execute 实现 StepPlugin，在本地将 source 打包为 tar.gz 或 zip
func (p *ArchivePlugin) Execute(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	source := maputil.GetString(step.With, "source")
	if source == "" {
		return fmt.Errorf("archive: source is required")
	}
	if ctx.Render != nil {
		source = ctx.Render(source)
	}
	source = ctx.ResolvePath(source)

	// 校验 source 存在
	info, err := os.Stat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("archive: source %q does not exist", source)
		}
		return fmt.Errorf("archive: stat source %q: %w", source, err)
	}

	dest := maputil.GetString(step.With, "dest")
	if dest == "" {
		dest = maputil.GetString(step.With, "output")
	}
	if dest == "" {
		dest = os.TempDir()
	}
	if ctx.Render != nil && dest != "" {
		dest = ctx.Render(dest)
	}
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return fmt.Errorf("archive: invalid dest %q: %w", dest, err)
	}
	if err := os.MkdirAll(destAbs, 0755); err != nil {
		return fmt.Errorf("archive: mkdir dest %q: %w", destAbs, err)
	}

	format := maputil.GetString(step.With, "format")
	if format == "" {
		format = "tar.gz"
	}
	if format != "tar.gz" && format != "zip" {
		return fmt.Errorf("archive: unsupported format %q (use tar.gz or zip)", format)
	}

	basename := maputil.GetString(step.With, "basename")
	if basename == "" {
		srcName := filepath.Base(source)
		if srcName == "." {
			srcName = "archive"
		}
		ts := time.Now().Format("20060102_150405")
		if format == "tar.gz" {
			basename = fmt.Sprintf("%s_%s.tar.gz", srcName, ts)
		} else {
			basename = fmt.Sprintf("%s_%s.zip", srcName, ts)
		}
	} else if ctx.Render != nil {
		basename = ctx.Render(basename)
	}
	// 确保 basename 不含路径
	basename = filepath.Base(basename)

	outputPath := filepath.Join(destAbs, basename)

	ctx.LogInfo(step.Name, "", fmt.Sprintf("Archiving %s -> %s", source, outputPath))

	// 执行压缩
	switch format {
	case "tar.gz":
		if err := p.writeTarGz(outputPath, source, info); err != nil {
			return err
		}
	case "zip":
		if err := p.writeZip(outputPath, source, info); err != nil {
			return err
		}
	}

	absPath, err := filepath.Abs(outputPath)
	if err != nil {
		return fmt.Errorf("archive: abs path %q: %w", outputPath, err)
	}

	ctx.LogInfo(step.Name, "", fmt.Sprintf("Archive done: %s", absPath))

	// 写入回滚数据
	ctx.SetRollbackData(step.Name, &rollbackData{OutputPath: absPath})
	return nil
}

// writeTarGz 将 source 打包为 tar.gz 写入本地文件
func (p *ArchivePlugin) writeTarGz(destPath, source string, info os.FileInfo) error {
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("archive: create %q: %w", destPath, err)
	}
	defer f.Close()
	gw := gzip.NewWriter(f)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()
	return p.writeTar(tw, source, info)
}

// writeTar 将 source 打包为 tar 写入 tw
func (p *ArchivePlugin) writeTar(tw *tar.Writer, source string, info os.FileInfo) error {
	if info.IsDir() {
		return filepath.Walk(source, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil
			}
			return p.addToTar(tw, path, rel, fi)
		})
	}
	return p.addToTar(tw, source, filepath.Base(source), info)
}

func (p *ArchivePlugin) addToTar(tw *tar.Writer, path, name string, info os.FileInfo) error {
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

// writeZip 将 source 打包为 zip 写入本地文件
func (p *ArchivePlugin) writeZip(destPath, source string, info os.FileInfo) error {
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("archive: create %q: %w", destPath, err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	if info.IsDir() {
		return filepath.Walk(source, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil
			}
			return p.addToZip(zw, path, rel, fi)
		})
	}
	return p.addToZip(zw, source, filepath.Base(source), info)
}

func (p *ArchivePlugin) addToZip(zw *zip.Writer, path, name string, info os.FileInfo) error {
	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	hdr.Name = name
	if info.IsDir() {
		hdr.Name += "/"
	}
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// Rollback 实现 StepPlugin，删除生成的压缩包
func (p *ArchivePlugin) Rollback(ctx *core.DeployContext, step core.Step) error {
	data, ok := ctx.GetRollbackData(step.Name)
	if !ok || data == nil {
		return nil
	}
	rd, ok := data.(*rollbackData)
	if !ok || rd == nil || rd.OutputPath == "" {
		return nil
	}
	_ = os.Remove(rd.OutputPath)
	return nil
}

// Uninstall 实现 StepPlugin，无状态插件，卸载时无需操作
func (p *ArchivePlugin) Uninstall(ctx *core.DeployContext, step core.Step, targets []core.Target) error {
	return nil
}
