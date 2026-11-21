package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/MACOS-DO/sub4api/internal/service"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// stagedUpgradeGrace is how long a staged upgrade journal must exist before the
// supervisor interrupts the running child. The grace period lets the HTTP
// response that reported the update reach the client before the listener goes
// away, and it makes sure the executable swap that follows the journal write
// has completed.
const stagedUpgradeGrace = 3 * time.Second

func runSupervisor() error {
	binary, e := os.Executable()
	if e != nil {
		return e
	}
	binary, e = filepath.EvalSymlinks(binary)
	if e != nil {
		return e
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	for {
		child := exec.Command(binary, "--serve-child")
		child.Env = append(os.Environ(), "IKUN_SUPERVISED_CHILD=1")
		child.Stdin = os.Stdin
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		j, jerr := service.ReadUpgradeJournal(binary)
		if jerr != nil && !os.IsNotExist(jerr) {
			return jerr
		}
		if j != nil {
			h, e := service.UpgradeFileHash(binary)
			if e != nil {
				return e
			}
			if h == j.PreviousSHA256 {
				// The process stopped after recording intent but before atomic swap.
				if err := os.Remove(service.UpgradeJournalPath(binary)); err != nil {
					return err
				}
				_ = os.Remove(service.UpgradeJournalPath(binary) + ".ready")
				log.Println("Interrupted before executable swap; continuing the previous version")
				continue
			}
			if h != j.CandidateSHA256 {
				return fmt.Errorf("candidate executable no longer matches upgrade journal")
			}
			if _, err := os.Stat(filepath.Join(j.BackupDirectory, "COMPLETE")); err != nil {
				hash, err := service.UpgradeFileHash(j.PreviousBinary)
				if err != nil || hash != j.PreviousSHA256 {
					return fmt.Errorf("previous executable integrity check failed")
				}
				backupCtx, backupCancel := context.WithTimeout(context.Background(), 30*time.Minute)
				command := exec.CommandContext(backupCtx, j.PreviousBinary, "--upgrade-backup-dir", j.BackupDirectory)
				command.Env = os.Environ()
				command.Stdout = os.Stdout
				command.Stderr = os.Stderr
				backupErr := command.Run()
				backupCancel()
				if err := backupErr; err != nil {
					if err := restorePreviousExecutable(binary, j); err != nil {
						return err
					}
					log.Println("Snapshot failed; previous executable restored before candidate startup")
					continue
				}
			}
		}
		if e := child.Start(); e != nil {
			if j != nil {
				return restoreSupervisedUpgrade(binary, j)
			}
			return e
		}
		done := make(chan error, 1)
		go func() { done <- child.Wait() }()
		tick := time.NewTicker(250 * time.Millisecond)
		deadline := time.Now().Add(3 * time.Minute)
		stopped := false
		failed := false
		for !stopped {
			select {
			case <-signals:
				_ = child.Process.Signal(syscall.SIGTERM)
				select {
				case <-done:
				case <-time.After(15 * time.Second):
					_ = child.Process.Kill()
					<-done
				}
				tick.Stop()
				return nil
			case werr := <-done:
				stopped = true
				if j != nil {
					// A clean exit while a candidate is staged is a restart request:
					// the swap already landed, so the next iteration starts the new
					// version and confirms it through the .ready marker. Anything
					// else (crash, non-zero status) is a failed startup.
					if werr == nil && upgradeCandidateInPlace(binary, j) {
						log.Println("Supervised child exited for a staged upgrade; starting the new version")
					} else {
						failed = true
					}
				}
			case <-tick.C:
				if j == nil {
					// The running child stages an update by writing the journal and
					// swapping the executable while it keeps serving traffic. Read the
					// journal again here, otherwise a staged upgrade would only be
					// noticed after somebody restarted the container by hand.
					staged, serr := service.ReadUpgradeJournal(binary)
					if serr != nil || staged == nil {
						continue
					}
					// Wait out the grace period so the HTTP response can reach the
					// client, and require the executable swap to have landed. Reading
					// the journal before the swap would otherwise look like an
					// interrupted upgrade and throw the staged candidate away.
					if time.Since(staged.CreatedAt) < stagedUpgradeGrace {
						continue
					}
					if !upgradeCandidateInPlace(binary, staged) {
						continue
					}
					log.Println("Upgrade staged by the running service; restarting into the new version")
					_ = child.Process.Signal(syscall.SIGTERM)
					select {
					case <-done:
					case <-time.After(15 * time.Second):
						_ = child.Process.Kill()
						<-done
					}
					// A staged upgrade is not a failure: the next iteration starts the
					// candidate binary and confirms it through the .ready marker.
					stopped = true
					failed = false
					continue
				}
				b, e := os.ReadFile(service.UpgradeJournalPath(binary) + ".ready")
				var ready struct {
					PID int    `json:"pid"`
					SHA string `json:"sha256"`
				}
				if e == nil && json.Unmarshal(b, &ready) == nil && ready.PID == child.Process.Pid && ready.SHA == j.CandidateSHA256 {
					_ = os.Remove(service.UpgradeJournalPath(binary))
					_ = os.Remove(service.UpgradeJournalPath(binary) + ".ready")
					j = nil
					log.Println("Upgrade startup confirmed; recovery snapshot retained")
				} else if time.Now().After(deadline) {
					_ = child.Process.Signal(syscall.SIGTERM)
					select {
					case <-done:
					case <-time.After(15 * time.Second):
						_ = child.Process.Kill()
						<-done
					}
					failed = true
					stopped = true
				}
			}
		}
		tick.Stop()
		if failed {
			if e := restoreSupervisedUpgrade(binary, j); e != nil {
				return e
			}
		}
		select {
		case <-signals:
			return nil
		case <-time.After(time.Second):
		}
	}
}
func restoreSupervisedUpgrade(binary string, j *service.UpgradeJournal) error {
	hash, e := service.UpgradeFileHash(j.PreviousBinary)
	if e != nil || hash != j.PreviousSHA256 {
		return fmt.Errorf("previous executable integrity check failed")
	}
	if _, e = os.Stat(filepath.Join(j.BackupDirectory, "COMPLETE")); e != nil {
		return fmt.Errorf("recovery snapshot incomplete")
	}
	log.Println("New version failed startup; restoring the pre-update database and executable")
	command := exec.Command(j.PreviousBinary, "--upgrade-restore-file", filepath.Join(j.BackupDirectory, "database.sql"))
	command.Env = os.Environ()
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if e = command.Run(); e != nil {
		return fmt.Errorf("database recovery failed; service remains stopped")
	}
	return restorePreviousExecutable(binary, j)
}
func restorePreviousExecutable(binary string, j *service.UpgradeJournal) error {
	data, e := os.ReadFile(j.PreviousBinary)
	if e != nil {
		return e
	}
	temp := binary + ".restoring"
	if e = os.WriteFile(temp, data, 0755); e != nil {
		return e
	}
	if e = os.Rename(temp, binary); e != nil {
		return e
	}
	if e = os.Remove(service.UpgradeJournalPath(binary)); e != nil {
		return e
	}
	_ = os.Remove(service.UpgradeJournalPath(binary) + ".ready")
	b, _ := json.Marshal(map[string]any{"phase": "failed", "error": "新版本未能启动或备份失败，已恢复升级前的程序与数据。", "active": false})
	_ = os.WriteFile(binary+".upgrade-result.json", b, 0600)
	return nil
}

// upgradeCandidateInPlace reports whether the staged candidate already replaced
// the live executable. It separates an intentional restart from an upgrade that
// never reached the atomic swap.
func upgradeCandidateInPlace(binary string, j *service.UpgradeJournal) bool {
	hash, err := service.UpgradeFileHash(binary)
	return err == nil && hash == j.CandidateSHA256
}
