package app

import (

	// "github.com/dutchcoders/dirbuster/vendor.bak/gopkg.in/src-d/go-git.v4/utils/ioutil"

	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"path"
	"strings"
	"sync/atomic"
	"time"

	cli "github.com/urfave/cli/v2"

	"github.com/fatih/color"
	_ "github.com/op/go-logging"
)

func (b *fuzzer) RecursiveFind(ctx *cli.Context, w []string, h []byte, r ArchiveReader) error {
	// should check for hashes if vulnerable or not
	for v := range r.Walk() {
		if ae, ok := v.(ArchiveError); ok {
			fmt.Fprintln(b.writer.Bypass(), color.RedString("[!][%s] could not traverse into %s \u001b[0K", strings.Join(w, " -> "), ae.Error()))

			b.stats.IncError()
			continue
		}

		f := v.(ArchiveFile)

		// only counting actual files
		if _, ok := (r.(*DirectoryReader)); ok {
			b.stats.IncFile()

			if b.verbose {
				fmt.Fprintln(b.writer.Bypass(), color.WhiteString("[!][%s] scanning %s \u001b[0K", strings.Join(append(w, f.Name()), " -> "), f.Name()))
			}
		}

		if err := func() error {
			if b.debug {
				fmt.Fprintln(b.writer.Bypass(), color.WhiteString("[!][%s] scanning %s \u001b[0K", strings.Join(append(w, f.Name()), " -> "), f.Name()))
			}

			// ignore files > 1GB
			size := f.FileInfo().Size()
			if size > 1073741824 {
				// skipping large file
				return nil
			} else if size == 0 {
				// skipping empty file
				return nil
			} else if size < 4 {
				// skipping small file
				return nil
			}

			rc, err := f.Open()
			if err != nil {
				return err
			}

			defer rc.Close()

			// calculate hash
			shaHash := sha256.New()

			if _, err := io.Copy(shaHash, rc); err != nil {
				b.stats.IncError()
				fmt.Fprintln(b.writer.Bypass(), color.RedString("[!][%s] could not calculate hash \u001b[0K", strings.Join(append(w, f.Name()), " -> ")))
				return err
			}

			hash := shaHash.Sum(nil)

			if version, ok := b.signatures[string(hash)]; !ok {
			} else if b.IsAllowList(hash) {
			} else {
				b.stats.IncVulnerableLibrary()

				fmt.Fprintln(b.writer.Bypass(), color.RedString("[!][%s] found vulnerable log4j with hash %x (version: %s) \u001b[0K", strings.Join(append(w, f.Name()), " -> "), hash, version))
			}

			rc.Seek(0, io.SeekStart)

			data := []byte{0, 0, 0, 0}
			if _, err := rc.Read(data); err != nil {
				b.stats.IncError()
				fmt.Fprintln(b.writer.Bypass(), color.RedString("[!][%s] could not read magic from file %x \u001b[0K", strings.Join(append(w, f.Name()), " -> "), hash))
				return err
			}

			rc.Seek(0, io.SeekStart)

			// check for PK signature
			if bytes.Compare(data[0:4], []byte{0x50, 0x4B, 0x03, 0x04}) == 0 {
				r2, err := NewZipArchiveReader(NewUnbufferedReaderAt(rc), size)
				if err != nil {
					b.stats.IncError()
					fmt.Fprintln(b.writer.Bypass(), color.RedString("[!][%s] could not open zip file %x \u001b[0K", strings.Join(append(w, f.Name()), " -> "), hash))
					return err
				}

				return b.RecursiveFind(ctx, append(w, f.Name()), hash, r2)
			} else if bytes.Compare(data[0:3], []byte{0x1F, 0x8B, 0x08}) == 0 {
				// tgz
				r2, err := NewGzipTARArchiveReader(NewUnbufferedReaderAt(rc), size)
				if err != nil {
					b.stats.IncError()
					fmt.Fprintln(b.writer.Bypass(), color.RedString("[!][%s] could not open tar file %x \u001b[0K", strings.Join(append(w, f.Name()), " -> "), hash))
					return err
				}

				return b.RecursiveFind(ctx, append(w, f.Name()), hash, r2)
			} else if found, _ := IsTAR(rc); found {
				// always test if file is a tar
				r2, err := NewTARArchiveReader(NewUnbufferedReaderAt(rc), size)
				if err != nil {
					b.stats.IncError()
					fmt.Fprintln(b.writer.Bypass(), color.RedString("[!][%s] could not open tar file %x \u001b[0K", strings.Join(append(w, f.Name()), " -> "), hash))
					return err
				}

				return b.RecursiveFind(ctx, append(w, f.Name()), hash, r2)
			} else {
				parts := strings.Split(path.Base(f.Name()), ".")
				if !strings.EqualFold(parts[0], "JndiLookup") {
					// not JndiLookup
				} else if bytes.Compare(data[0:4], []byte{0xCA, 0xFE, 0xBA, 0xBE}) != 0 /* class file */ {
					// not a class file
				} else {
					// todo(remco): we need to pass hashes, so we can keep log4j2
					// can we patch / replace log4j with 2.16?
					version := "unknown"
					if v, ok := b.signatures[string(h)]; ok {
						version = v
					}

					if !b.IsAllowList(h) {
						b.stats.IncVulnerableFile()
						// TODO(REMCO) : improve message!
						fmt.Fprintln(b.writer.Bypass(), color.RedString("[!][%s] found JndiLookup class file with hash %x (version: %s) \u001b[0K", strings.Join(append(w, f.Name()), " -> "), h, version))
					}
				}

				return nil
			}
		}(); err != nil {
			fmt.Fprintln(b.writer.Bypass(), color.RedString("[!][ ] Error while scanning: %s => %s \u001b[0K", strings.Join(w, "->"), err))
		}
	}

	return nil
}
func (b *fuzzer) Scan(ctx *cli.Context) error {
	ch := make(chan interface{})
	defer close(ch)

	b.writer.Start()
	defer b.writer.Stop() // flush and stop rendering

	start := time.Now()
	go func() {
		for {
			sub := time.Now().Sub(start)

			select {
			case <-ch:
				return
			default:
			}

			i := b.stats.Files()

			fmt.Fprintf(b.writer, color.GreenString("[ ] Checked %d files in %02.fh%02.fm%02.fs, average rate is: %0.f files/min. \u001b[0K\n", atomic.LoadUint64(&i), sub.Seconds()/3600, sub.Seconds()/60, sub.Seconds(), float64(i)/sub.Minutes()))
			time.Sleep(time.Millisecond * 100)
		}
	}()

	for _, target := range b.targetPaths {
		dr, err := NewDirectoryReader(target, b.excludeList)
		if err != nil {
			fmt.Fprintf(b.writer.Bypass(), color.RedString("[ ] Could not walk into %s: %s\u001b[0K\n", target, err))
		}

		if err := b.RecursiveFind(ctx, []string{}, []byte{}, dr); err != nil {
			fmt.Fprintf(b.writer.Bypass(), color.RedString("[ ] Could not walk into %s: %s\u001b[0K\n", target, err))
		}
	}

	i := b.stats.Files()
	sub := time.Now().Sub(start)
	fmt.Fprintln(b.writer.Bypass(), color.YellowString("[🏎]: Scan finished! %d files scanned, %d vulnerable files found, %d vulnerable libraries found, %d errors occured,  in %02.fh%02.fm%02.fs, average rate is: %0.f files/min. \u001b[0K", i, b.stats.VulnerableFiles(), b.stats.VulnerableLibraries(), b.stats.Errors(), sub.Seconds()/3600, sub.Seconds()/60, sub.Seconds(), float64(i)/sub.Minutes()))
	return nil
}
