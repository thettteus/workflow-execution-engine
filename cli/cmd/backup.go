package cmd

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newBackupCmd() *cobra.Command {
	var workspace string
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Back up or restore the workspace state directory",
		Long: "Back up or restore the workspace state directory: executions, artifacts,\n" +
			"cache, and non-secret settings. Stop wee serve before restoring.",
	}
	cmd.PersistentFlags().StringVar(&workspace, "workspace", workspaceDir, "workspace state directory")

	create := &cobra.Command{
		Use:   "create <archive.tar.gz>",
		Short: "Create a compressed backup of the workspace state directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := createBackup(workspace, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "backup created: %s (%d files)\n", args[0], n)
			return nil
		},
	}

	var force bool
	restore := &cobra.Command{
		Use:   "restore <archive.tar.gz>",
		Short: "Restore a workspace backup into --workspace",
		Long: "Restore refuses to write into a non-empty workspace unless --force is set.\n" +
			"Stop wee serve before restoring so no process writes while files are replaced.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := restoreBackup(args[0], workspace, force)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "backup restored into %s (%d files)\n", workspace, n)
			return nil
		},
	}
	restore.Flags().BoolVar(&force, "force", false, "restore even when --workspace already contains files")

	cmd.AddCommand(create, restore)
	return cmd
}

func createBackup(workspace, archivePath string) (int, error) {
	info, err := os.Stat(workspace)
	if err != nil {
		return 0, fmt.Errorf("workspace %s is not readable: %w", workspace, err)
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("workspace %s is not a directory", workspace)
	}
	if inside, err := pathInside(workspace, archivePath); err != nil {
		return 0, err
	} else if inside {
		return 0, fmt.Errorf("backup archive must be outside workspace %s", workspace)
	}
	out, err := os.Create(archivePath)
	if err != nil {
		return 0, fmt.Errorf("create backup %s: %w", archivePath, err)
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	files := 0
	err = filepath.WalkDir(workspace, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == workspace {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(workspace, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = name
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		if _, err := io.Copy(tw, in); err != nil {
			_ = in.Close()
			return err
		}
		if err := in.Close(); err != nil {
			return err
		}
		files++
		return nil
	})
	if err != nil {
		return files, fmt.Errorf("write backup: %w", err)
	}
	return files, nil
}

func restoreBackup(archivePath, workspace string, force bool) (int, error) {
	if !force {
		empty, err := dirEmpty(workspace)
		if err != nil {
			return 0, err
		}
		if !empty {
			return 0, fmt.Errorf("workspace %s is not empty; pass --force to restore over it", workspace)
		}
	}
	in, err := os.Open(archivePath)
	if err != nil {
		return 0, fmt.Errorf("open backup %s: %w", archivePath, err)
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return 0, fmt.Errorf("read backup %s: %w", archivePath, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	root, err := filepath.Abs(workspace)
	if err != nil {
		return 0, err
	}
	files := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return files, fmt.Errorf("read backup entry: %w", err)
		}
		target, err := safeRestorePath(root, h.Name)
		if err != nil {
			return files, err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(h.Mode)); err != nil {
				return files, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return files, err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode))
			if err != nil {
				return files, err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return files, err
			}
			if err := out.Close(); err != nil {
				return files, err
			}
			files++
		default:
			return files, fmt.Errorf("unsupported backup entry %s", h.Name)
		}
	}
	return files, nil
}

func dirEmpty(path string) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("open workspace %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Readdirnames(1); errors.Is(err, io.EOF) {
		return true, nil
	} else if err != nil {
		return false, fmt.Errorf("read workspace %s: %w", path, err)
	}
	return false, nil
}

func safeRestorePath(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, "\\") {
		return "", fmt.Errorf("unsafe backup entry %q", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe backup entry %q", name)
	}
	target := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe backup entry %q", name)
	}
	return target, nil
}

func pathInside(parent, child string) (bool, error) {
	p, err := filepath.Abs(parent)
	if err != nil {
		return false, err
	}
	c, err := filepath.Abs(child)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(p, c)
	if err != nil {
		return false, err
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)), nil
}
