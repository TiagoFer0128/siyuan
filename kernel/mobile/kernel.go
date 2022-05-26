// SiYuan - Build Your Eternal Digital Garden
// Copyright (c) 2020-present, b3log.org
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package mobile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/siyuan-note/siyuan/kernel/model"
	"github.com/siyuan-note/siyuan/kernel/server"
	"github.com/siyuan-note/siyuan/kernel/sql"
	"github.com/siyuan-note/siyuan/kernel/treenode"
	"github.com/siyuan-note/siyuan/kernel/util"
	_ "golang.org/x/mobile/bind"
)

func StartKernelFast(container, appDir, workspaceDir, nativeLibDir, privateDataDir, localIP string) {
	go server.Serve(true)
}

func StartKernel(container, appDir, workspaceDir, nativeLibDir, privateDataDir, timezoneID, localIPs, lang string) {
	SetTimezone(container, appDir, timezoneID)
	util.Mode = "prod"

	util.LocalIPs = strings.Split(localIPs, ",")
	util.BootMobile(container, appDir, workspaceDir, nativeLibDir, privateDataDir, lang)

	model.InitConf()
	go server.Serve(false)
	go func() {
		model.InitAppearance()
		sql.InitDatabase(false)
		sql.SetCaseSensitive(model.Conf.Search.CaseSensitive)

		model.SyncData(true, false, false)
		model.InitBoxes()

		go model.AutoGenerateDocHistory()
		go model.AutoSync()
		go model.AutoStat()
		util.SetBooted()
		util.ClearPushProgress(100)
		go model.AutoRefreshUser()
		go model.AutoFlushTx()
		go sql.AutoFlushTreeQueue()
		go treenode.AutoFlushBlockTree()
	}()
}

func Language(num int) string {
	return model.Conf.Language(num)
}

func ShowMsg(msg string, timeout int) {
	util.PushMsg(msg, timeout)
}

func IsHttpServing() bool {
	return util.HttpServing
}

func SetTimezone(container, appDir, timezoneID string) {
	if "ios" == container {
		os.Setenv("ZONEINFO", filepath.Join(appDir, "app", "zoneinfo.zip"))
	}
	z, err := time.LoadLocation(strings.TrimSpace(timezoneID))
	if err != nil {
		fmt.Printf("load location failed: %s\n", err)
		time.Local = time.FixedZone("CST", 8*3600)
		return
	}
	time.Local = z
}
