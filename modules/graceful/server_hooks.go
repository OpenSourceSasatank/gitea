// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package graceful

import (
	"os"

	"code.gitea.io/gitea/modules/log"
)

// 【読み順 STEP 7】サーバーの停止フック — Manager からのシグナルを Server に伝達。
//   1. awaitShutdown(): IsShutdown() or IsHammer() を select で待機
//   2. doShutdown(): リスナーを Close し、新規接続の受付を停止
//   3. doHammer(): 残存コネクションを強制切断
//   STEP 6（server.go）の Server 構造体と連携して動作する。
//
// awaitShutdown waits for the shutdown signal from the Manager
func (srv *Server) awaitShutdown() {
	select {
	case <-GetManager().IsShutdown():
		// Shutdown
		srv.doShutdown()
	case <-GetManager().IsHammer():
		// Hammer
		srv.doShutdown()
		srv.doHammer()
	}
	<-GetManager().IsHammer()
	srv.doHammer()
}

// shutdown closes the listener so that no new connections are accepted
// and starts a goroutine that will hammer (stop all running requests) the server
// after setting.GracefulHammerTime.
func (srv *Server) doShutdown() {
	// only shutdown if we're running.
	if srv.getState() != stateRunning {
		return
	}

	srv.setState(stateShuttingDown)

	if srv.OnShutdown != nil {
		srv.OnShutdown()
	}
	err := srv.listener.Close()
	if err != nil {
		log.Error("PID: %d Listener.Close() error: %v", os.Getpid(), err)
	} else {
		log.Info("PID: %d Listener (%s) closed.", os.Getpid(), srv.listener.Addr())
	}
}

func (srv *Server) doHammer() {
	if srv.getState() != stateShuttingDown {
		return
	}
	srv.closeAllConnections()
}
