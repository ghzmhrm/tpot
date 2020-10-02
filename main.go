package main

import (
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/adzimzf/tpot/config"
	scapper "github.com/adzimzf/tpot/scrapper"
	"github.com/adzimzf/tpot/tsh"
	"github.com/adzimzf/tpot/ui"
	"github.com/spf13/cobra"
)

func main() {
	rootCmd.Flags().BoolP("refresh", "r", false, "refresh the node list")
	rootCmd.Flags().BoolP("append", "a", false, "append the node list, if there's same node will be ignored")
	rootCmd.Flags().BoolP("cfg", "c", false, "add config")
	if err := rootCmd.Execute(); err != nil {
		log.Fatalf("failed to execute :%v", err)
	}
}

var example = `tpot staging     // Show the node list of staging environment
tpot prod -c     // Set up the configuration for production environment
tpot prod -a     // Get the latest node list then append to the cache for production 
tpot prod -r     // Refresh the cache with the latest node from Teleport UI
`

var rootCmd = &cobra.Command{
	Use:     "tpot <environment>",
	Short:   "tpot is tsh teleport wrapper",
	Long:    `config file is inside ` + os.Getenv("HOME") + `/.tpot/`,
	Example: example,
	Run: func(cmd *cobra.Command, args []string) {
		isCfg, err := cmd.Flags().GetBool("cfg")
		if err != nil {
			cmd.PrintErrln("failed to get config due to ", err)
			return
		}
		if isCfg {
			addConfig(cmd, args)
			return
		}

		if len(args) < 1 {
			cmd.Help()
			return
		}

		proxy, err := config.NewProxy(args[0])
		if errors.Is(err, config.ErrEnvNotFound) {
			cmd.PrintErrf("Env %s not found\n\n", args[0])
			cmd.Help()
			return
		}
		if os.IsNotExist(err) {
			cmd.PrintErrln("Config not found\nrun tpot -c to add new proxy config")
			return
		}
		if err != nil {
			cmd.PrintErrln("failed to get config due to ", err)
			return
		}

		nodesItem, err := getNodeItems(cmd, proxy)
		if err != nil {
			cmd.PrintErrln(err)
			return
		}

		host := ui.GetSelectedHost(nodesItem)
		if host == "" {
			cmd.PrintErrln("Pick at least one host to login")
			return
		}
		err = tsh.NewTSH(proxy, host).Run()
		if err != nil {
			cmd.PrintErrln(err)
		}
	},
}

func addConfig(cmd *cobra.Command, args []string) {
	err := config.AddConfig()
	if err != nil {
		cmd.PrintErr(err)
	}
}

func getNodeItems(cmd *cobra.Command, proxy *config.Proxy) ([]string, error) {
	isRefresh, err := cmd.Flags().GetBool("refresh")
	if err != nil {
		return nil, err
	}
	isAppend, err := cmd.Flags().GetBool("append")
	if err != nil {
		return nil, err
	}
	var nodes config.Node
	if isRefresh || isAppend {
		nodes, err = getLatestNode(proxy, isAppend)
		if err != nil {
			return nil, err
		}
	} else {
		nodes, err = proxy.GetNode()
		if err != nil {
			return nil, fmt.Errorf("failed to load nodes %v,\nyour might need -r to refresh/add the node cache", err)
		}
	}

	// update the latest proxy to latest nodes
	proxy.Node = nodes

	var pItems []string
	for _, n := range nodes.Items {
		pItems = append(pItems, n.Hostname)
	}
	return pItems, nil
}

func getLatestNode(proxy *config.Proxy, isAppend bool) (config.Node, error) {
	nodes, err := scapper.NewScrapper(*proxy).GetNodes()
	if err != nil {
		return nodes, fmt.Errorf("failed to get nodes: %v", err)
	}
	if isAppend {
		nodes, err = proxy.AppendNode(nodes)
		if err != nil {
			return nodes, fmt.Errorf("failed to append nodes, err: %v", err)
		}
	}
	go proxy.UpdateNode(nodes)
	return nodes, nil
}
