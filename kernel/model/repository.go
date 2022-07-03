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

package model

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/88250/gulu"
	"github.com/siyuan-note/dejavu"
	"github.com/siyuan-note/dejavu/entity"
	"github.com/siyuan-note/encryption"
	"github.com/siyuan-note/eventbus"
	"github.com/siyuan-note/filelock"
	"github.com/siyuan-note/siyuan/kernel/cache"
	"github.com/siyuan-note/siyuan/kernel/sql"
	"github.com/siyuan-note/siyuan/kernel/util"
)

func init() {
	subscribeEvents()
}

func GetRepoIndexLogs(page int) (logs []*dejavu.Log, pageCount, totalCount int, err error) {
	if 1 > len(Conf.Repo.Key) {
		err = errors.New(Conf.Language(26))
		return
	}

	repo, err := newRepository()
	if nil != err {
		return
	}

	logs, pageCount, totalCount, err = repo.GetIndexLogs(page, 32)
	if nil != err {
		if dejavu.ErrNotFoundIndex == err {
			logs = []*dejavu.Log{}
			err = nil
			return
		}

		util.LogErrorf("get data repo index logs failed: %s", err)
		return
	}
	return
}

func ImportRepoKey(base64Key string) (err error) {
	msgId := util.PushMsg(Conf.Language(136), 1000*7)

	key, err := base64.StdEncoding.DecodeString(base64Key)
	if nil != err {
		return
	}
	Conf.Repo.Key = key
	Conf.Save()

	if err = os.RemoveAll(Conf.Repo.GetSaveDir()); nil != err {
		return
	}
	if err = os.MkdirAll(Conf.Repo.GetSaveDir(), 0755); nil != err {
		return
	}

	time.Sleep(1 * time.Second)
	util.PushUpdateMsg(msgId, Conf.Language(138), 3000)
	time.Sleep(1 * time.Second)
	if initErr := IndexRepo("Init data repo"); nil != initErr {
		util.PushUpdateMsg(msgId, fmt.Sprintf(Conf.Language(140), initErr), 7000)
	}
	return
}

func ResetRepo() (err error) {
	msgId := util.PushMsg(Conf.Language(144), 1000*60)

	if err = os.RemoveAll(Conf.Repo.GetSaveDir()); nil != err {
		return
	}
	if err = os.MkdirAll(Conf.Repo.GetSaveDir(), 0755); nil != err {
		return
	}

	Conf.Repo.Key = nil
	Conf.Save()

	util.PushUpdateMsg(msgId, Conf.Language(145), 3000)
	return
}

func InitRepoKey() (err error) {
	msgId := util.PushMsg(Conf.Language(136), 1000*7)

	if err = os.RemoveAll(Conf.Repo.GetSaveDir()); nil != err {
		return
	}
	if err = os.MkdirAll(Conf.Repo.GetSaveDir(), 0755); nil != err {
		return
	}

	randomBytes := make([]byte, 16)
	_, err = rand.Read(randomBytes)
	if nil != err {
		return
	}
	password := string(randomBytes)
	randomBytes = make([]byte, 16)
	_, err = rand.Read(randomBytes)
	if nil != err {
		util.LogErrorf("init data repo key failed: %s", err)
		util.PushUpdateMsg(msgId, Conf.Language(137), 5000)
		return
	}
	salt := string(randomBytes)

	key, err := encryption.KDF(password, salt)
	if nil != err {
		util.LogErrorf("init data repo key failed: %s", err)
		util.PushUpdateMsg(msgId, Conf.Language(137), 5000)
		return
	}
	Conf.Repo.Key = key
	Conf.Save()

	time.Sleep(1 * time.Second)
	util.PushUpdateMsg(msgId, Conf.Language(138), 3000)
	time.Sleep(1 * time.Second)
	if initErr := IndexRepo("Init data repo"); nil != initErr {
		util.PushUpdateMsg(msgId, fmt.Sprintf(Conf.Language(140), initErr), 7000)
	}
	return
}

func CheckoutRepo(id string) (err error) {
	if 1 > len(Conf.Repo.Key) {
		err = errors.New(Conf.Language(26))
		return
	}

	repo, err := newRepository()
	if nil != err {
		return
	}

	util.PushEndlessProgress(Conf.Language(63))
	writingDataLock.Lock()
	defer writingDataLock.Unlock()
	WaitForWritingFiles()
	sql.WaitForWritingDatabase()
	filelock.ReleaseAllFileLocks()
	CloseWatchAssets()
	defer WatchAssets()

	// 恢复快照时自动暂停同步，避免刚刚恢复后的数据又被同步覆盖
	syncEnabled := Conf.Sync.Enabled
	Conf.Sync.Enabled = false
	Conf.Save()

	_, _, err = repo.Checkout(id, map[string]interface{}{
		CtxPushMsg: CtxPushMsgToStatusBarAndProgress,
	})
	if nil != err {
		util.PushClearProgress()
		return
	}

	RefreshFileTree()
	if syncEnabled {
		func() {
			time.Sleep(5 * time.Second)
			util.PushMsg(Conf.Language(134), 0)
		}()
	}
	return
}

func IndexRepo(memo string) (err error) {
	if 1 > len(Conf.Repo.Key) {
		err = errors.New(Conf.Language(26))
		return
	}

	memo = gulu.Str.RemoveInvisible(memo)
	if "" == memo {
		err = errors.New(Conf.Language(142))
		return
	}

	repo, err := newRepository()
	if nil != err {
		return
	}

	util.PushEndlessProgress(Conf.Language(143))
	writingDataLock.Lock()
	defer writingDataLock.Unlock()

	start := time.Now()
	latest, _ := repo.Latest()
	WaitForWritingFiles()
	filelock.ReleaseAllFileLocks()
	index, err := repo.Index(memo, map[string]interface{}{
		CtxPushMsg: CtxPushMsgToStatusBarAndProgress,
	})
	if nil != err {
		util.PushStatusBar("Create data snapshot failed")
		return
	}
	elapsed := time.Since(start)

	if nil != latest {
		if latest.ID != index.ID {
			util.PushStatusBar(fmt.Sprintf(Conf.Language(147)+" [%s]", elapsed.Seconds(), latest.ID[:7]))
		} else {
			util.PushStatusBar(Conf.Language(148) + " [" + latest.ID[:7] + "]")
		}
	} else {
		util.PushStatusBar(fmt.Sprintf(Conf.Language(147)+" [%s]", elapsed.Seconds(), latest.ID[:7]))
	}
	util.PushClearProgress()
	return
}

const (
	CtxPushMsg = "pushMsg"

	CtxPushMsgToProgress = iota
	CtxPushMsgToStatusBar
	CtxPushMsgToStatusBarAndProgress
)

func indexRepoBeforeCloudSync() {
	if 1 > len(Conf.Repo.Key) {
		return
	}

	repo, err := newRepository()
	if nil != err {
		return
	}

	start := time.Now()
	latest, _ := repo.Latest()
	index, err := repo.Index("[Auto] Cloud sync", map[string]interface{}{
		CtxPushMsg: CtxPushMsgToStatusBar,
	})
	if nil != err {
		util.PushStatusBar("Create data snapshot for cloud sync failed")
		util.LogErrorf("index data repo before cloud sync failed: %s", err)
		return
	}
	elapsed := time.Since(start)
	if nil != latest {
		if latest.ID != index.ID {
			// 对新创建的快照需要更新备注，加入耗时统计
			index.Memo = fmt.Sprintf("[Auto] Cloud sync, completed in %.2fs", elapsed.Seconds())
			err = repo.PutIndex(index)
			if nil != err {
				util.PushStatusBar("Save data snapshot for cloud sync failed")
				util.LogErrorf("put index into data repo before cloud sync failed: %s", err)
				return
			}
			util.PushStatusBar(fmt.Sprintf(Conf.Language(147)+" [%s]", elapsed.Seconds(), latest.ID[:7]))
		} else {
			util.PushStatusBar(Conf.Language(148) + " [" + latest.ID[:7] + "]")
		}
	} else {
		util.PushStatusBar(fmt.Sprintf(Conf.Language(147)+" [%s]", elapsed.Seconds(), latest.ID[:7]))
	}
	if 7000 < elapsed.Milliseconds() {
		util.LogWarnf("index data repo before cloud sync elapsed [%dms]", elapsed.Milliseconds())
	}
}

func syncRepo(byHand bool) {
	if 1 > len(Conf.Repo.Key) {
		return
	}

	repo, err := newRepository()
	if nil != err {
		util.LogErrorf("sync repo failed: %s", err)
		return
	}

	CloseWatchAssets()
	defer WatchAssets()

	start := time.Now()
	cloudInfo := &dejavu.CloudInfo{
		Dir:       "main",
		UserID:    Conf.User.UserId,
		Token:     Conf.User.UserToken,
		LimitSize: int64(Conf.User.UserSiYuanRepoSize - Conf.User.UserSiYuanAssetSize),
		ProxyURL:  Conf.System.NetworkProxy.String(),
		Server:    util.AliyunServer,
	}
	syncContext := map[string]interface{}{CtxPushMsg: CtxPushMsgToStatusBar}
	latest, mergeUpserts, mergeRemoves, _, err := repo.Sync(cloudInfo, syncContext)

	elapsed := time.Since(start)
	util.LogInfof("sync data repo elapsed [%.2fs], latest [%s]", elapsed.Seconds(), latest.ID)
	if nil != err {
		util.LogErrorf("sync data repo failed: %s", err)
		msg := "Sync data repo failed: " + err.Error()
		if errors.Is(err, dejavu.ErrSyncCloudStorageSizeExceeded) {
			msg = fmt.Sprintf(Conf.Language(43), byteCountSI(int64(Conf.User.UserSiYuanRepoSize)))
		}
		util.PushStatusBar(msg)
		util.PushErrMsg(msg, 0)
		return
	}
	util.PushStatusBar(fmt.Sprintf(Conf.Language(149)+" [%s]", elapsed.Seconds(), latest.ID[:7]))

	if 1 > len(mergeUpserts) && 1 > len(mergeRemoves) { // 没有数据变更
		syncSameCount++
		if 10 < syncSameCount {
			syncSameCount = 5
		}
		if !byHand {
			delay := time.Minute * time.Duration(int(math.Pow(2, float64(syncSameCount))))
			if fixSyncInterval.Minutes() > delay.Minutes() {
				delay = time.Minute * 8
			}
			planSyncAfter(delay)
		}
		return
	}

	// 有数据变更，需要重建索引
	var upserts, removes []string
	for _, file := range mergeUpserts {
		upserts = append(upserts, file.Path)
	}
	for _, file := range mergeRemoves {
		removes = append(removes, file.Path)
	}
	incReindex(upserts, removes)
	cache.ClearDocsIAL()

	// 刷新界面
	util.ReloadUI()
	elapsed = time.Since(start)
	go func() {
		time.Sleep(2 * time.Second)
		util.PushStatusBar(fmt.Sprintf(Conf.Language(149)+" [%s]", elapsed.Seconds(), latest.ID[:7]))
	}()
	return
}

func newRepository() (ret *dejavu.Repo, err error) {
	ignoreLines := getIgnoreLines()
	ignoreLines = append(ignoreLines, "/.siyuan/conf.json") // 忽略旧版同步配置
	ret, err = dejavu.NewRepo(util.DataDir, util.RepoDir, util.HistoryDir, util.TempDir, Conf.Repo.Key, ignoreLines)
	if nil != err {
		util.LogErrorf("init data repository failed: %s", err)
	}
	return
}

func subscribeEvents() {
	eventbus.Subscribe(dejavu.EvtIndexWalkData, func(context map[string]interface{}, path string) {
		msg := "Indexing data repository [walk data " + path + "]"
		util.SetBootDetails(msg)
		contextPushMsg(context, msg)
	})
	eventbus.Subscribe(dejavu.EvtIndexGetLatestFile, func(context map[string]interface{}, path string) {
		msg := "Indexing data repository [get latest file " + path + "]"
		util.SetBootDetails(msg)
		contextPushMsg(context, msg)
	})
	eventbus.Subscribe(dejavu.EvtIndexUpsertFile, func(context map[string]interface{}, path string) {
		msg := "Indexing data repository [upsert file " + path + "]"
		util.SetBootDetails(msg)
		contextPushMsg(context, msg)
	})

	eventbus.Subscribe(dejavu.EvtCheckoutWalkData, func(context map[string]interface{}, path string) {
		msg := "Checkout data repository [walk data " + path + "]"
		util.SetBootDetails(msg)
		contextPushMsg(context, msg)
	})
	eventbus.Subscribe(dejavu.EvtCheckoutUpsertFile, func(context map[string]interface{}, path string) {
		msg := "Checkout data repository [upsert file " + path + "]"
		util.SetBootDetails(msg)
		contextPushMsg(context, msg)
	})
	eventbus.Subscribe(dejavu.EvtCheckoutRemoveFile, func(context map[string]interface{}, path string) {
		msg := "Checkout data repository [remove file " + path + "]"
		util.SetBootDetails(msg)
		contextPushMsg(context, msg)
	})

	eventbus.Subscribe(dejavu.EvtSyncBeforeDownloadCloudLatest, func(context map[string]interface{}) {
		msg := "Downloading data repository latest..."
		util.SetBootDetails(msg)
		contextPushMsg(context, msg)
	})

	eventbus.Subscribe(dejavu.EvtSyncAfterDownloadCloudLatest, func(context map[string]interface{}, latest *entity.Index) {
		msg := fmt.Sprintf("Downloaded latest [%s]", latest.ID[:7])
		util.SetBootDetails(msg)
		contextPushMsg(context, msg)

		//util.LogInfof(msg)
		//for _, index := range indexes {
		//	util.LogInfof("    [%s]", index.ID)
		//}
	})

	eventbus.Subscribe(dejavu.EvtSyncBeforeDownloadCloudFile, func(context map[string]interface{}, id string) {
		msg := "Downloading data repository object [" + id + "]"
		util.SetBootDetails(msg)
		contextPushMsg(context, msg)
	})

	eventbus.Subscribe(dejavu.EvtSyncBeforeDownloadCloudChunk, func(context map[string]interface{}, id string) {
		msg := "Downloading data repository object [" + id + "]"
		util.SetBootDetails(msg)
		contextPushMsg(context, msg)
	})

	eventbus.Subscribe(dejavu.EvtSyncBeforeUploadObject, func(context map[string]interface{}, id string) {
		msg := "Uploading data repository object [" + id + "]"
		util.SetBootDetails(msg)
		contextPushMsg(context, msg)
	})
}

func contextPushMsg(context map[string]interface{}, msg string) {
	switch context[CtxPushMsg].(int) {
	case CtxPushMsgToProgress:
		util.PushEndlessProgress(msg)
	case CtxPushMsgToStatusBar:
		util.PushStatusBar(msg)
	case CtxPushMsgToStatusBarAndProgress:
		util.PushStatusBar(msg)
		util.PushEndlessProgress(msg)
	}
}
