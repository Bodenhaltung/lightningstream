package commands

import (
	"context"
	"os"

	"github.com/PowerDNS/simpleblob"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/wojas/go-healthz"
	"golang.org/x/sync/errgroup"
	"powerdns.com/platform/lightningstream/status"
	"powerdns.com/platform/lightningstream/syncer"
	"powerdns.com/platform/lightningstream/utils"
)

var (
	onlyOnce   bool
	markerFile string
)

func init() {
	rootCmd.AddCommand(syncCmd)
	syncCmd.Flags().BoolVar(&onlyOnce, "only-once", false, "Only do a single run and exit")
	syncCmd.Flags().StringVar(&markerFile, "wait-for-marker-file", "", "Marker file to wait for in storage before starting syncers")
}

func runSync() error {
	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()

	if onlyOnce {
		conf.OnlyOnce = true
	}

	st, err := simpleblob.GetBackend(ctx, conf.Storage.Type, conf.Storage.Options)
	if err != nil {
		return err
	}
	logrus.WithField("storage_type", conf.Storage.Type).Info("Storage backend initialised")
	status.SetStorage(st)

	// If enabled, wait for marker file to be present in storage before starting syncers
	if markerFile != "" {
		logrus.Infof("waiting for marker file '%s' to be present in storage", markerFile)
		for {
			if _, err := st.Load(ctx, markerFile); err == nil {
				logrus.Infof("marker file '%s' found, proceeding", markerFile)
				break
			} else {
				if !os.IsNotExist(err) {
					logrus.WithError(err).Errorf("unable to check storage for marker file '%s'", markerFile)
				}
			}

			logrus.Debugf("waiting for marker file '%s'", markerFile)

			if err := utils.SleepContext(ctx, conf.StoragePollInterval); err != nil {
				return err
			}
		}
	}

	eg, ctx := errgroup.WithContext(ctx)
	for name, lc := range conf.LMDBs {
		s, err := syncer.New(name, st, conf, lc)
		if err != nil {
			return err
		}

		name := name
		eg.Go(func() error {
			err := s.Sync(ctx)
			if err != nil {
				if err == context.Canceled {
					logrus.WithField("db", name).Error("Sync cancelled")
					return err
				}
				logrus.WithError(err).WithField("db", name).Error("Sync failed")
			}
			return err
		})
	}

	healthz.AddBuildInfo()
	if hostname, err := os.Hostname(); err == nil {
		healthz.SetMeta("hostname", hostname)
	}
	healthz.SetMeta("version", version)

	if !conf.OnlyOnce {
		status.StartHTTPServer(conf)
	} else {
		logrus.Info("Not starting the HTTP server, because OnlyOnce is set")
	}

	logrus.Info("All syncers running")
	return eg.Wait()
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Continuous bidirectional syncing",
	Run: func(cmd *cobra.Command, args []string) {
		if err := runSync(); err != nil {
			logrus.WithError(err).Fatal("Error")
		}
	},
}
