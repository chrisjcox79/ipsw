/*
Copyright © 2019 blacktop

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
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/apex/log"
	"github.com/blacktop/ipsw/internal/download"
	"github.com/blacktop/ipsw/internal/utils"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func init() {
	downloadCmd.AddCommand(betaCmd)
}

// betaCmd represents the beta command
var betaCmd = &cobra.Command{
	Use:   "beta [build-id]",
	Short: "Download beta IPSWs from the iphone wiki",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var filteredURLS []string
		if Verbose {
			log.SetLevel(log.DebugLevel)
		}

		proxy, _ := cmd.Flags().GetString("proxy")
		insecure, _ := cmd.Flags().GetBool("insecure")
		device, _ := cmd.Flags().GetString("device")
		skip, _ := cmd.Flags().GetBool("yes")
		removeCommas, _ := cmd.Flags().GetBool("remove-commas")

		ipsws, err := download.ScrapeURLs(args[0])
		if err != nil {
			return errors.Wrap(err, "failed to query www.theiphonewiki.com")
		}

		if len(ipsws) < 1 {
			log.Errorf("no ipsws found for build %s", args[0])
			return nil
		}

		for url, ipsw := range ipsws {
			if len(device) > 0 {
				if utils.StrSliceContains(ipsw.Devices, device) {
					filteredURLS = append(filteredURLS, url)
				}
			} else {
				filteredURLS = append(filteredURLS, url)
			}
		}

		if len(filteredURLS) < 1 {
			log.Errorf("no ipsws match device %s", device)
			return nil
		}

		log.Debug("URLs to Download:")
		for _, url := range filteredURLS {
			utils.Indent(log.Debug, 2)(url)
		}

		cont := true
		if !skip {
			cont = false
			prompt := &survey.Confirm{
				Message: fmt.Sprintf("You are about to download %d ipsw files. Continue?", len(filteredURLS)),
			}
			survey.AskOne(prompt, &cont)
		}

		if cont {
			downloader := download.NewDownload(proxy, insecure)
			for _, url := range filteredURLS {
				var destName string
				if removeCommas {
					destName = strings.Replace(path.Base(url), ",", "_", -1)
				} else {
					destName = path.Base(url)
				}
				if _, err := os.Stat(destName); os.IsNotExist(err) {
					log.WithFields(log.Fields{
						"devices": ipsws[url].Devices,
						"build":   ipsws[url].BuildID,
						"version": ipsws[url].Version,
					}).Info("Getting IPSW")
					// download file
					downloader.URL = url
					downloader.RemoveCommas = removeCommas
					err = downloader.Do()
					if err != nil {
						return errors.Wrap(err, "failed to download file")
					}
				} else {
					log.Warnf("ipsw already exists: %s", destName)
				}
			}
		}

		return nil
	},
}
