/*
 * Copyright © 2020 nicksherron <nsherron90@gmail.com>
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// findCmd represents the stats command
var (
	findCmd = &cobra.Command{
		Use:   "find",
		Short: "Find the record for a proxy",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return errors.New("requires a proxy argument")
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Flags().Parse(args)
			findProxy(strings.TrimSpace(args[0]))
		},
	}
)

func init() {
	rootCmd.AddCommand(findCmd)
	findCmd.PersistentFlags().StringVarP(&address, "url", "u", fmt.Sprintf("http://%v", listenAddr()), "Url of running ProxyPool server.")
}
