/*
Copyright © 2021 blacktop

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
*/
package cmd

import (
	"bytes"
	"compress/gzip"
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"

	"github.com/apex/log"
	"github.com/blacktop/go-arm64"
	"github.com/blacktop/ipsw/internal/utils"
	"github.com/blacktop/ipsw/pkg/dyld"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func init() {
	dyldCmd.AddCommand(dyldDisassCmd)

	// dyldDisassCmd.Flags().StringP("symbol", "s", "", "Function to disassemble")
	// dyldDisassCmd.Flags().Uint64P("vaddr", "a", 0, "Virtual address to start disassembling")
	dyldDisassCmd.Flags().Uint64P("count", "c", 0, "Number of instructions to disassemble")
	dyldDisassCmd.Flags().BoolVarP(&demangleFlag, "demangle", "d", false, "Demandle symbol names")
	// dyldDisassCmd.Flags().StringP("sym-file", "s", "", "Companion symbol map file")
	dyldDisassCmd.Flags().StringP("image", "i", "", "dylib image to search")

	symaddrCmd.MarkZshCompPositionalArgumentFile(1, "dyld_shared_cache*")
}

// disassCmd represents the disass command
var dyldDisassCmd = &cobra.Command{
	Use:   "disass",
	Short: "🚧 [WIP] Disassemble dyld_shared_cache symbol in an image",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {

		var data []byte

		if Verbose {
			log.SetLevel(log.DebugLevel)
		}

		imageName, _ := cmd.Flags().GetString("image")
		instructions, _ := cmd.Flags().GetUint64("count")

		// symbolName, _ := cmd.Flags().GetString("symbol")
		// doDemangle, _ := cmd.Flags().GetBool("demangle")

		dscPath := filepath.Clean(args[0])

		fileInfo, err := os.Lstat(dscPath)
		if err != nil {
			return fmt.Errorf("file %s does not exist", dscPath)
		}

		// Check if file is a symlink
		if fileInfo.Mode()&os.ModeSymlink != 0 {
			symlinkPath, err := os.Readlink(dscPath)
			if err != nil {
				return errors.Wrapf(err, "failed to read symlink %s", dscPath)
			}
			// TODO: this seems like it would break
			linkParent := filepath.Dir(dscPath)
			linkRoot := filepath.Dir(linkParent)

			dscPath = filepath.Join(linkRoot, symlinkPath)
		}

		f, err := dyld.Open(dscPath, &dyld.Config{ParsePatchInfo: true})
		if err != nil {
			return err
		}
		defer f.Close()

		// Load all symbols
		if _, err := os.Stat(dscPath + ".a2s"); os.IsNotExist(err) {
			log.Info("Generating dyld_shared_cache companion symbol map file...")

			utils.Indent(log.Warn, 2)("parsing public symbols...")
			err = f.GetAllExportedSymbols(false)
			if err != nil {
				return err
			}
			utils.Indent(log.Warn, 2)("parsing private symbols...")
			err = f.ParseLocalSyms()
			if err != nil {
				utils.Indent(log.Warn, 2)(err.Error())
				utils.Indent(log.Warn, 2)("parsing patch exports...")
				for _, img := range f.Images {
					for _, patch := range img.PatchableExports {
						addr, err := f.GetVMAddress(uint64(patch.OffsetOfImpl))
						if err != nil {
							return err
						}
						f.AddressToSymbol[addr] = patch.Name
					}
				}
			}

			// save lookup map to disk to speed up subsequent requests
			err = f.SaveAddrToSymMap(dscPath + ".a2s")
			if err != nil {
				return err
			}

		} else {
			log.Info("Found dyld_shared_cache companion symbol map file...")
			a2sFile, err := os.Open(dscPath + ".a2s")
			if err != nil {
				return fmt.Errorf("failed to open companion file %s; %v", dscPath+".a2s", err)
			}

			gzr, err := gzip.NewReader(a2sFile)
			if err != nil {
				return fmt.Errorf("failed to create gzip reader: %v", err)
			}

			// Decoding the serialized data
			err = gob.NewDecoder(gzr).Decode(&f.AddressToSymbol)
			if err != nil {
				return fmt.Errorf("failed to decode addr2sym map; %v", err)
			}
			gzr.Close()
			a2sFile.Close()
		}

		if len(args) > 1 {
			log.Info("Locating symbol: " + args[1])
			symAddr, image, err := f.GetSymbolAddress(args[1], imageName)
			if err != nil {
				return err
			}

			off, _ := f.GetOffset(symAddr)

			if image == nil {
				image, err = f.GetImageContainingTextAddr(symAddr)
				if err != nil {
					return err
				}
			}

			log.WithFields(log.Fields{"dylib": image.Name}).Info("Found symbol")

			m, err := image.GetPartialMacho()
			if err != nil {
				return err
			}

			if f.LocalSymbolsOffset == 0 {
				utils.Indent(log.Warn, 2)("parsing symbol table...")
				for _, sym := range m.Symtab.Syms {
					if sym.Value != 0 {
						f.AddressToSymbol[sym.Value] = sym.Name
					}
				}
			}
			// fmt.Println(m.FileTOC.String())

			starts := m.FunctionStartAddrs()

			if instructions > 0 {
				data, err = f.ReadBytes(int64(off), instructions*4)
				if err != nil {
					return err
				}
			} else {
				data, err = f.ReadBytes(int64(off), uint64(f.FunctionSize(starts, symAddr)))
				if err != nil {
					return err
				}
			}

			if m.HasObjC() {
				log.Info("Parsing ObjC runtime structures...")
				err = f.CFStringsForImage(image.Name)
				if err != nil {
					return errors.Wrapf(err, "failed to parse objc cfstrings")
				}
				err = f.MethodsForImage(image.Name)
				if err != nil {
					return errors.Wrapf(err, "failed to parse objc methods")
				}
				err = f.SelectorsForImage(image.Name)
				// _, err = f.AllSelectors(false)
				if err != nil {
					return errors.Wrapf(err, "failed to parse objc selectors")
				}
				err = f.ClassesForImage(image.Name)
				if err != nil {
					return errors.Wrapf(err, "failed to parse objc classes")
				}
			}

			log.Info("Parsing MachO symbol stubs...")
			err = f.ParseSymbolStubs(m)
			if err != nil {
				return errors.Wrapf(err, "failed to parse symbol stubs")
			}

			log.Info("Parsing MachO global offset table...")
			err = f.ParseGOT(m)
			if err != nil {
				return errors.Wrapf(err, "failed to parse GOT")
			}

			var prevInstruction arm64.Instruction

			for i := range arm64.Disassemble(bytes.NewReader(data), arm64.Options{StartAddress: int64(symAddr)}) {

				if i.Error != nil {
					fmt.Println(i.StrRepr)
					continue
				}

				opStr := i.Instruction.OpStr()

				// check for start of a new function
				if yes, fname := f.IsFunctionStart(starts, i.Instruction.Address(), demangleFlag); yes {
					if len(fname) > 0 {
						fmt.Printf("\n%s:\n", fname)
					} else {
						fmt.Printf("\nfunc_%x:\n", i.Instruction.Address())
					}
				}

				// lookup adrp/ldr or add address as a cstring or symbol name
				operation := i.Instruction.Operation().String()
				if (operation == "ldr" || operation == "add") && prevInstruction.Operation().String() == "adrp" {
					operands := i.Instruction.Operands()
					if operands != nil && prevInstruction.Operands() != nil {
						adrpRegister := prevInstruction.Operands()[0].Reg[0]
						adrpImm := prevInstruction.Operands()[1].Immediate
						if operation == "ldr" && adrpRegister == operands[1].Reg[0] {
							adrpImm += operands[1].Immediate
						} else if operation == "add" && adrpRegister == operands[1].Reg[0] {
							adrpImm += operands[2].Immediate
						}
						// markup disassemble with label comment
						symName := f.FindSymbol(adrpImm, demangleFlag)
						if len(symName) > 0 {
							opStr += fmt.Sprintf(" ; %s", symName)
						} else {
							cstr, err := f.IsCString(m, adrpImm)
							if err == nil {
								if len(cstr) > 200 {
									opStr += fmt.Sprintf(" ; %#v...", cstr[:200])
								} else {
									opStr += fmt.Sprintf(" ; %#v", cstr)
								}
							}
						}
					}

				} else if i.Instruction.Group() == arm64.GROUP_BRANCH_EXCEPTION_SYSTEM { // check if branch location is a function
					operands := i.Instruction.Operands()
					if operands != nil && operands[0].OpClass == arm64.LABEL {
						symName := f.FindSymbol(operands[0].Immediate, demangleFlag)
						if len(symName) > 0 {
							opStr = fmt.Sprintf("\t%s", symName)
						}
					}
				} else if i.Instruction.Group() == arm64.GROUP_DATA_PROCESSING_IMM || i.Instruction.Group() == arm64.GROUP_LOAD_STORE {
					operation := i.Instruction.Operation()
					if operation == arm64.ARM64_LDR || operation == arm64.ARM64_ADR {
						operands := i.Instruction.Operands()
						if operands[1].OpClass == arm64.LABEL {
							symName := f.FindSymbol(operands[1].Immediate, demangleFlag)
							if len(symName) > 0 {
								opStr += fmt.Sprintf(" ; %s", symName)
							}
						}
					}
				}

				fmt.Printf("%#08x:  %s\t%s%s%s\n", i.Instruction.Address(), i.Instruction.OpCodes(), i.Instruction.Operation(), pad(10-len(i.Instruction.Operation().String())), opStr)

				prevInstruction = *i.Instruction
			}

			return nil
		}

		return fmt.Errorf("you must supply a cache and a symbol to disassemble")
	},
}
