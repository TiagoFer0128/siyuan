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
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/88250/gulu"
	"github.com/panjf2000/ants/v2"
	"github.com/qiniu/go-sdk/v7/storage"
	"github.com/siyuan-note/siyuan/kernel/sql"
	"github.com/siyuan-note/siyuan/kernel/treenode"
	"github.com/siyuan-note/siyuan/kernel/util"
)

func getCloudSpaceOSS() (sync, backup map[string]interface{}, assetSize int64, err error) {
	result := map[string]interface{}{}
	request := util.NewCloudRequest(Conf.System.NetworkProxy.String())
	resp, err := request.
		SetResult(&result).
		SetBody(map[string]string{"token": Conf.User.UserToken}).
		Post(util.AliyunServer + "/apis/siyuan/data/getSiYuanWorkspace")
	if nil != err {
		util.LogErrorf("get cloud space failed: %s", err)
		return nil, nil, 0, ErrFailedToConnectCloudServer
	}

	if 401 == resp.StatusCode {
		err = errors.New(Conf.Language(31))
		return
	}

	code := result["code"].(float64)
	if 0 != code {
		util.LogErrorf("get cloud space failed: %s", result["msg"])
		return nil, nil, 0, errors.New(result["msg"].(string))
	}

	data := result["data"].(map[string]interface{})
	sync = data["sync"].(map[string]interface{})
	backup = data["backup"].(map[string]interface{})
	assetSize = int64(data["assetSize"].(float64))
	return
}

func removeCloudDirPath(dirPath string) (err error) {
	result := map[string]interface{}{}
	request := util.NewCloudRequest(Conf.System.NetworkProxy.String())
	resp, err := request.
		SetResult(&result).
		SetBody(map[string]string{"dirPath": dirPath, "token": Conf.User.UserToken}).
		Post(util.AliyunServer + "/apis/siyuan/data/removeSiYuanDirPath")
	if nil != err {
		util.LogErrorf("create cloud sync dir failed: %s", err)
		return ErrFailedToConnectCloudServer
	}

	if 200 != resp.StatusCode {
		if 401 == resp.StatusCode {
			err = errors.New(Conf.Language(31))
			return
		}
		msg := fmt.Sprintf("remove cloud dir failed: %d", resp.StatusCode)
		util.LogErrorf(msg)
		err = errors.New(msg)
		return
	}
	return
}

func createCloudSyncDirOSS(name string) (err error) {
	result := map[string]interface{}{}
	request := util.NewCloudRequest(Conf.System.NetworkProxy.String())
	resp, err := request.
		SetResult(&result).
		SetBody(map[string]string{"name": name, "token": Conf.User.UserToken}).
		Post(util.AliyunServer + "/apis/siyuan/data/createSiYuanSyncDir")
	if nil != err {
		util.LogErrorf("create cloud sync dir failed: %s", err)
		return ErrFailedToConnectCloudServer
	}

	if 200 != resp.StatusCode {
		if 401 == resp.StatusCode {
			err = errors.New(Conf.Language(31))
			return
		}
		msg := fmt.Sprintf("create cloud sync dir failed: %d", resp.StatusCode)
		util.LogErrorf(msg)
		err = errors.New(msg)
		return
	}

	code := result["code"].(float64)
	if 0 != code {
		util.LogErrorf("create cloud sync dir failed: %s", result["msg"])
		return errors.New(result["msg"].(string))
	}
	return
}

func listCloudSyncDirOSS() (dirs []map[string]interface{}, size int64, err error) {
	result := map[string]interface{}{}
	request := util.NewCloudRequest(Conf.System.NetworkProxy.String())
	resp, err := request.
		SetBody(map[string]interface{}{"token": Conf.User.UserToken}).
		SetResult(&result).
		Post(util.AliyunServer + "/apis/siyuan/data/getSiYuanSyncDirList?uid=" + Conf.User.UserId)
	if nil != err {
		util.LogErrorf("get cloud sync dirs failed: %s", err)
		return nil, 0, ErrFailedToConnectCloudServer
	}

	if 200 != resp.StatusCode {
		if 401 == resp.StatusCode {
			err = errors.New(Conf.Language(31))
			return
		}
		msg := fmt.Sprintf("get cloud sync dirs failed: %d", resp.StatusCode)
		util.LogErrorf(msg)
		err = errors.New(msg)
		return
	}

	code := result["code"].(float64)
	if 0 != code {
		util.LogErrorf("get cloud sync dirs failed: %s", result["msg"])
		return nil, 0, ErrFailedToConnectCloudServer
	}

	data := result["data"].(map[string]interface{})
	dataDirs := data["dirs"].([]interface{})
	for _, d := range dataDirs {
		dirs = append(dirs, d.(map[string]interface{}))
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i]["name"].(string) < dirs[j]["name"].(string) })
	size = int64(data["size"].(float64))
	return
}

func ossDownload(isBackup bool, localDirPath, cloudDirPath string, bootOrExit bool, removeList, upsertList map[string]bool) (fetchedFilesCount int, transferSize uint64, downloadedFiles map[string]bool, err error) {
	if !gulu.File.IsExist(localDirPath) {
		return
	}

	cloudFileList, err := getCloudFileListOSS(cloudDirPath)
	if nil != err {
		return
	}

	// 将云端索引文件临时保存一下，后面下载数据时如果部分成功，需要用索引文件恢复部分成功的文件 syncDirUpsertWorkspaceData()
	data, err := gulu.JSON.MarshalJSON(cloudFileList)
	if nil != err {
		return
	}
	tmpSyncDir := filepath.Join(util.TempDir, "sync")
	tmpIndex := filepath.Join(tmpSyncDir, "index.json")
	if err = os.WriteFile(tmpIndex, data, 0644); nil != err {
		return
	}

	if !isBackup && (0 < len(removeList) || 0 < len(upsertList)) {
		// 上传合并本地变更

		var removed, upserted bool
		var removes []string
		for remove, _ := range removeList {
			removes = append(removes, remove)
		}
		err = ossRemove0(cloudDirPath, removes)
		if nil != err {
			util.LogErrorf("remove merge cloud file failed: %s", err)
			return
		}
		for remove, _ := range removeList {
			delete(cloudFileList, remove)
		}
		removed = 0 < len(removeList)

		var tmpWroteFiles int
		var tmpTransferSize uint64
		for upsert, _ := range upsertList {
			localUpsert := filepath.Join(localDirPath, upsert)
			var info os.FileInfo
			info, err = os.Stat(localUpsert)
			if nil != err {
				util.LogErrorf("stat file [%s] failed: %s", localUpsert, err)
				return
			}

			if err = ossUpload0(localDirPath, cloudDirPath, localUpsert, &tmpWroteFiles, &tmpTransferSize); nil != err {
				util.LogErrorf("upload merge cloud file [%s] failed: %s", upsert, err)
				return
			}
			cloudFileList[upsert] = &CloudIndex{
				Size:    info.Size(),
				Updated: info.ModTime().Unix(),
			}
			upserted = true
		}

		if removed || upserted {
			data, err = gulu.JSON.MarshalJSON(cloudFileList)
			if nil != err {
				util.LogErrorf("marshal cloud file list failed: %s", err)
				return
			}
			if err = os.WriteFile(tmpIndex, data, 0644); nil != err {
				util.LogErrorf("write cloud file list failed: %s", err)
				return
			}
			if err = ossUpload0(tmpSyncDir, cloudDirPath, tmpIndex, &tmpWroteFiles, &tmpTransferSize); nil != err {
				util.LogErrorf("upload merge cloud file [%s] failed: %s", tmpIndex, err)
				return
			}
		}
	}

	localRemoves, cloudFetches, err := localUpsertRemoveListOSS(localDirPath, cloudFileList)
	if nil != err {
		return
	}

	for _, localRemove := range localRemoves {
		if err = os.RemoveAll(localRemove); nil != err {
			util.LogErrorf("local remove [%s] failed: %s", localRemove, err)
			return
		}
	}

	needPushProgress := 32 < len(cloudFetches)
	waitGroup := &sync.WaitGroup{}
	var downloadErr error
	downloadedFilesLock := sync.Mutex{}
	downloadedFiles = map[string]bool{}
	poolSize := 4
	if poolSize > len(cloudFetches)-1 /* 不计入 /.siyuan/conf.json，配置文件最后单独下载 */ {
		poolSize = len(cloudFetches)
	}
	p, _ := ants.NewPoolWithFunc(poolSize, func(arg interface{}) {
		defer waitGroup.Done()
		if nil != downloadErr {
			return // 快速失败
		}

		fetch := arg.(string)
		err = ossDownload0(localDirPath, cloudDirPath, fetch, &fetchedFilesCount, &transferSize, bootOrExit)
		if nil != err {
			downloadErr = err // 仅记录最后一次错误
			return
		}
		downloadedFilesLock.Lock()
		downloadedFiles[fetch] = true
		downloadedFilesLock.Unlock()

		if needPushProgress {
			msg := fmt.Sprintf(Conf.Language(103), fetchedFilesCount, len(cloudFetches)-fetchedFilesCount)
			util.PushProgress(util.PushProgressCodeProgressed, fetchedFilesCount, len(cloudFetches), msg)
		}
		if bootOrExit {
			msg := fmt.Sprintf("Downloading data from the cloud %d/%d", fetchedFilesCount, len(cloudFetches))
			util.IncBootProgress(0, msg)
		}
	})
	for _, fetch := range cloudFetches {
		if "/.siyuan/conf.json" == fetch {
			// 同步下载可能会报错，为了确保本地数据版本号不变所以不能更新配置文件，配置文件最后单独下载
			continue
		}
		if "/"+pathJSON == fetch {
			// 已经在前面验证解密的步骤中下载过了，目前位于 temp/sync/pathJSON
			continue
		}

		waitGroup.Add(1)
		p.Invoke(fetch)
	}
	waitGroup.Wait()
	p.Release()
	if nil != downloadErr {
		err = downloadErr
		return
	}

	if "backup" != cloudDirPath {
		err = ossDownload0(localDirPath, cloudDirPath, "/.siyuan/conf.json", &fetchedFilesCount, &transferSize, bootOrExit)
		if nil != err {
			return
		}
	}

	if needPushProgress {
		util.ClearPushProgress(len(cloudFetches))
		util.PushMsg(Conf.Language(106), 1000*60*10)
	}
	if bootOrExit {
		util.IncBootProgress(0, "Decrypting from sync to data...")
	}
	return
}

func ossDownload0(localDirPath, cloudDirPath, fetch string, fetchedFiles *int, transferSize *uint64, bootORExit bool) (err error) {
	localFilePath := filepath.Join(localDirPath, fetch)
	remoteFileURL := path.Join(cloudDirPath, fetch)
	var result map[string]interface{}
	resp, err := util.NewCloudRequest(Conf.System.NetworkProxy.String()).
		SetResult(&result).
		SetBody(map[string]interface{}{"token": Conf.User.UserToken, "path": remoteFileURL}).
		Post(util.AliyunServer + "/apis/siyuan/data/getSiYuanFile?uid=" + Conf.User.UserId)
	if nil != err {
		util.LogErrorf("download request [%s] failed: %s", remoteFileURL, err)
		return errors.New(fmt.Sprintf(Conf.Language(93), err))
	}

	if 200 != resp.StatusCode {
		if 401 == resp.StatusCode {
			err = errors.New("account authentication failed, please login again")
			return errors.New(fmt.Sprintf(Conf.Language(93), err))
		}
		util.LogErrorf("download request status code [%d]", resp.StatusCode)
		err = errors.New("download file URL failed")
		return errors.New(fmt.Sprintf(Conf.Language(93), err))
	}

	code := result["code"].(float64)
	if 0 != code {
		msg := result["msg"].(string)
		util.LogErrorf("download cloud file failed: %s", msg)
		return errors.New(fmt.Sprintf(Conf.Language(93), msg))
	}

	resultData := result["data"].(map[string]interface{})
	downloadURL := resultData["url"].(string)

	if err = os.MkdirAll(filepath.Dir(localFilePath), 0755); nil != err {
		return
	}
	os.Remove(localFilePath)

	if bootORExit {
		resp, err = util.NewCloudFileRequest15s(Conf.System.NetworkProxy.String()).Get(downloadURL)
	} else {
		resp, err = util.NewCloudFileRequest2m(Conf.System.NetworkProxy.String()).Get(downloadURL)
	}
	if nil != err {
		util.LogErrorf("download request [%s] failed: %s", downloadURL, err)
		return errors.New(fmt.Sprintf(Conf.Language(93), err))
	}
	if 200 != resp.StatusCode {
		util.LogErrorf("download request [%s] status code [%d]", downloadURL, resp.StatusCode)
		err = errors.New(fmt.Sprintf("download file failed [%d]", resp.StatusCode))
		if 404 == resp.StatusCode {
			err = errors.New(Conf.Language(135))
		}
		return errors.New(fmt.Sprintf(Conf.Language(93), err))
	}

	data, err := resp.ToBytes()
	if nil != err {
		util.LogErrorf("download read response body data failed: %s, %s", err, string(data))
		err = errors.New("download read data failed")
		return errors.New(fmt.Sprintf(Conf.Language(93), err))
	}
	size := int64(len(data))

	if err = gulu.File.WriteFileSafer(localFilePath, data, 0644); nil != err {
		util.LogErrorf("write file [%s] failed: %s", localFilePath, err)
		return errors.New(fmt.Sprintf(Conf.Language(93), err))
	}

	*fetchedFiles++
	*transferSize += uint64(size)
	return
}

func ossUpload(isBackup bool, localDirPath, cloudDirPath, cloudDevice string, boot bool, removeList, upsertList map[string]bool) (wroteFiles int, transferSize uint64, err error) {
	if !gulu.File.IsExist(localDirPath) {
		return
	}

	localDevice := Conf.System.ID
	excludes := getSyncExcludedList(localDirPath)
	localFileList, genIndexErr := genCloudIndex(localDirPath, excludes)
	if nil != genIndexErr {
		err = genIndexErr
		return
	}

	var localUpserts, cloudRemoves []string
	var cloudFileList map[string]*CloudIndex
	var downloadList map[string]bool
	if "" != localDevice && localDevice == cloudDevice && !isBackup {
		//util.LogInfof("cloud device is the same as local device, get index from local")
		localUpserts, cloudRemoves, err = cloudUpsertRemoveLocalListOSS(localDirPath, removeList, upsertList, excludes)
	} else {
		cloudFileList, err = getCloudFileListOSS(cloudDirPath)
	}
	if nil != err {
		return
	}

	if 0 < len(cloudFileList) {
		localUpserts, cloudRemoves, downloadList, err = cloudUpsertRemoveListOSS(localDirPath, cloudFileList, localFileList, removeList, upsertList, excludes)
		if nil != err {
			return
		}
		if 0 < len(downloadList) && !isBackup {
			// 下载合并云端变更

			var data []byte
			data, err = gulu.JSON.MarshalJSON(cloudFileList)
			if nil != err {
				return
			}
			indexPath := filepath.Join(util.TempDir, "sync", "index.json")
			if err = os.WriteFile(indexPath, data, 0644); nil != err {
				return
			}

			var tmpFetchedFiles int
			var tmpTransferSize uint64
			err = ossDownload0(util.TempDir+"/sync", "sync/"+Conf.Sync.CloudName, "/"+pathJSON, &tmpFetchedFiles, &tmpTransferSize, false)
			if nil != err {
				util.LogErrorf("download merge cloud file failed: %s", err)
				return
			}

			metaPath := filepath.Join(util.TempDir, "/sync/"+pathJSON)
			err = syncDirUpsertWorkspaceData(metaPath, indexPath, downloadList)
			if nil != err {
				util.LogErrorf("download merge cloud file failed: %s", err)
				return
			}
			// 增量索引
			for upsertFile, _ := range downloadList {
				if !strings.HasSuffix(upsertFile, ".sy") {
					continue
				}

				upsertFile = filepath.ToSlash(upsertFile)
				box := upsertFile[:strings.Index(upsertFile, "/")]
				p := strings.TrimPrefix(upsertFile, box)
				tree, err0 := LoadTree(box, p)
				if nil != err0 {
					continue
				}
				treenode.ReindexBlockTree(tree)
				sql.UpsertTreeQueue(tree)
			}

			// 重新生成云端索引
			if _, err = genCloudIndex(localDirPath, excludes); nil != err {
				return
			}
		}
	}

	err = ossRemove0(cloudDirPath, cloudRemoves)
	if nil != err {
		return
	}

	needPushProgress := 32 < len(localUpserts)
	waitGroup := &sync.WaitGroup{}
	var uploadErr error

	poolSize := 4
	if poolSize > len(localUpserts) {
		poolSize = len(localUpserts)
	}
	p, _ := ants.NewPoolWithFunc(poolSize, func(arg interface{}) {
		defer waitGroup.Done()
		if nil != uploadErr {
			return // 快速失败
		}
		localUpsert := arg.(string)
		err = ossUpload0(localDirPath, cloudDirPath, localUpsert, &wroteFiles, &transferSize)
		if nil != err {
			uploadErr = err
			return
		}
		if needPushProgress {
			util.PushMsg(fmt.Sprintf(Conf.Language(104), wroteFiles, len(localUpserts)-wroteFiles), 1000*60*10)
		}
		if boot {
			msg := fmt.Sprintf("Uploading data to the cloud %d/%d", wroteFiles, len(localUpserts))
			util.IncBootProgress(0, msg)
		}
	})
	index := filepath.Join(localDirPath, "index.json")
	meta := filepath.Join(localDirPath, pathJSON)
	for _, localUpsert := range localUpserts {
		if index == localUpsert || meta == localUpsert {
			// 同步过程中断导致的一致性问题 https://github.com/siyuan-note/siyuan/issues/4912
			// index 和路径映射文件最后单独上传
			continue
		}

		waitGroup.Add(1)
		p.Invoke(localUpsert)
	}
	waitGroup.Wait()
	p.Release()
	if nil != uploadErr {
		err = uploadErr
		return
	}

	// 单独上传 index 和路径映射
	if uploadErr = ossUpload0(localDirPath, cloudDirPath, index, &wroteFiles, &transferSize); nil != uploadErr {
		err = uploadErr
		return
	}
	if uploadErr = ossUpload0(localDirPath, cloudDirPath, meta, &wroteFiles, &transferSize); nil != uploadErr {
		err = uploadErr
		return
	}

	if needPushProgress {
		util.PushMsg(Conf.Language(105), 3000)
	}
	return
}

func ossRemove0(cloudDirPath string, removes []string) (err error) {
	if 1 > len(removes) {
		return
	}

	request := util.NewCloudRequest(Conf.System.NetworkProxy.String())
	resp, err := request.
		SetBody(map[string]interface{}{"token": Conf.User.UserToken, "dirPath": cloudDirPath, "paths": removes}).
		Post(util.AliyunServer + "/apis/siyuan/data/removeSiYuanFile?uid=" + Conf.User.UserId)
	if nil != err {
		util.LogErrorf("remove cloud file failed: %s", err)
		err = ErrFailedToConnectCloudServer
		return
	}

	if 401 == resp.StatusCode {
		err = errors.New(Conf.Language(31))
		return
	}

	if 200 != resp.StatusCode {
		msg := fmt.Sprintf("remove cloud file failed [sc=%d]", resp.StatusCode)
		util.LogErrorf(msg)
		err = errors.New(msg)
		return
	}
	return
}

func ossUpload0(localDirPath, cloudDirPath, localUpsert string, wroteFiles *int, transferSize *uint64) (err error) {
	info, statErr := os.Stat(localUpsert)
	if nil != statErr {
		util.LogErrorf("stat file [%s] failed: %s", localUpsert, statErr)
		err = statErr
		return
	}

	filename := filepath.ToSlash(strings.TrimPrefix(localUpsert, localDirPath))
	upToken, err := getOssUploadToken(filename, cloudDirPath, info.Size())
	if nil != err {
		return
	}

	key := path.Join("siyuan", Conf.User.UserId, cloudDirPath, filename)
	if err = putFileToCloud(localUpsert, key, upToken); nil != err {
		util.LogErrorf("put file [%s] to cloud failed: %s", localUpsert, err)
		return errors.New(fmt.Sprintf(Conf.Language(94), err))
	}

	//util.LogInfof("cloud wrote [%s], size [%d]", filename, info.Size())
	*wroteFiles++
	*transferSize += uint64(info.Size())
	return
}

func getOssUploadToken(filename, cloudDirPath string, length int64) (ret string, err error) {
	// 因为需要指定 key，所以每次上传文件都必须在云端生成 Token，否则有安全隐患

	var result map[string]interface{}
	req := util.NewCloudRequest(Conf.System.NetworkProxy.String()).
		SetResult(&result)
	req.SetBody(map[string]interface{}{
		"token":   Conf.User.UserToken,
		"dirPath": cloudDirPath,
		"name":    filename,
		"length":  length})
	resp, err := req.Post(util.AliyunServer + "/apis/siyuan/data/getSiYuanFileUploadToken?uid=" + Conf.User.UserId)
	if nil != err {
		util.LogErrorf("get file [%s] upload token failed: %+v", filename, err)
		err = errors.New(fmt.Sprintf(Conf.Language(94), err))
		return
	}

	if 200 != resp.StatusCode {
		if 401 == resp.StatusCode {
			err = errors.New(fmt.Sprintf(Conf.Language(94), Conf.Language(31)))
			return
		}
		util.LogErrorf("get file [%s] upload token failed [sc=%d]", filename, resp.StatusCode)
		err = errors.New(fmt.Sprintf(Conf.Language(94), strconv.Itoa(resp.StatusCode)))
		return
	}

	code := result["code"].(float64)
	if 0 != code {
		msg := result["msg"].(string)
		util.LogErrorf("download cloud file failed: %s", msg)
		err = errors.New(fmt.Sprintf(Conf.Language(93), msg))
		return
	}

	resultData := result["data"].(map[string]interface{})
	ret = resultData["token"].(string)
	return
}

func getCloudSyncVer(cloudDir string) (cloudSyncVer int64, err error) {
	start := time.Now()
	result := map[string]interface{}{}
	request := util.NewCloudRequest(Conf.System.NetworkProxy.String())
	resp, err := request.
		SetResult(&result).
		SetBody(map[string]string{"syncDir": cloudDir, "token": Conf.User.UserToken}).
		Post(util.AliyunServer + "/apis/siyuan/data/getSiYuanWorkspaceSyncVer?uid=" + Conf.User.UserId)
	if nil != err {
		util.LogErrorf("get cloud sync ver failed: %s", err)
		err = ErrFailedToConnectCloudServer
		return
	}
	if 200 != resp.StatusCode {
		if 401 == resp.StatusCode {
			err = errors.New(Conf.Language(31))
			return
		}
		util.LogErrorf("get cloud sync ver failed: %d", resp.StatusCode)
		err = ErrFailedToConnectCloudServer
		return
	}

	code := result["code"].(float64)
	if 0 != code {
		msg := result["msg"].(string)
		util.LogErrorf("get cloud sync ver failed: %s", msg)
		err = errors.New(msg)
		return
	}

	data := result["data"].(map[string]interface{})
	cloudSyncVer = int64(data["v"].(float64))

	if elapsed := time.Now().Sub(start).Milliseconds(); 2000 < elapsed {
		util.LogInfof("get cloud sync ver elapsed [%dms]", elapsed)
	}
	return
}

func getCloudSync(cloudDir string) (assetSize, backupSize int64, device string, err error) {
	start := time.Now()
	result := map[string]interface{}{}
	request := util.NewCloudRequest(Conf.System.NetworkProxy.String())
	resp, err := request.
		SetResult(&result).
		SetBody(map[string]string{"syncDir": cloudDir, "token": Conf.User.UserToken}).
		Post(util.AliyunServer + "/apis/siyuan/data/getSiYuanWorkspaceSync?uid=" + Conf.User.UserId)
	if nil != err {
		util.LogErrorf("get cloud sync info failed: %s", err)
		err = ErrFailedToConnectCloudServer
		return
	}
	if 200 != resp.StatusCode {
		if 401 == resp.StatusCode {
			err = errors.New(Conf.Language(31))
			return
		}
		util.LogErrorf("get cloud sync info failed: %d", resp.StatusCode)
		err = ErrFailedToConnectCloudServer
		return
	}

	code := result["code"].(float64)
	if 0 != code {
		msg := result["msg"].(string)
		util.LogErrorf("get cloud sync info failed: %s", msg)
		err = errors.New(msg)
		return
	}

	data := result["data"].(map[string]interface{})
	assetSize = int64(data["assetSize"].(float64))
	backupSize = int64(data["backupSize"].(float64))
	if nil != data["d"] {
		device = data["d"].(string)
	}

	if elapsed := time.Now().Sub(start).Milliseconds(); 5000 < elapsed {
		util.LogInfof("get cloud sync [%s] elapsed [%dms]", elapsed)
	}
	return
}

func getCloudFileListOSS(cloudDirPath string) (ret map[string]*CloudIndex, err error) {
	result := map[string]interface{}{}
	request := util.NewCloudRequest(Conf.System.NetworkProxy.String())
	resp, err := request.
		SetResult(&result).
		SetBody(map[string]string{"dirPath": cloudDirPath, "token": Conf.User.UserToken}).
		Post(util.AliyunServer + "/apis/siyuan/data/getSiYuanFileListURL?uid=" + Conf.User.UserId)
	if nil != err {
		util.LogErrorf("get cloud file list failed: %s", err)
		err = ErrFailedToConnectCloudServer
		return
	}

	if 401 == resp.StatusCode {
		err = errors.New(Conf.Language(31))
		return
	}

	code := result["code"].(float64)
	if 0 != code {
		util.LogErrorf("get cloud file list failed: %s", result["msg"])
		err = ErrFailedToConnectCloudServer
		return
	}

	retData := result["data"].(map[string]interface{})
	downloadURL := retData["url"].(string)
	resp, err = util.NewCloudFileRequest15s(Conf.System.NetworkProxy.String()).Get(downloadURL)
	if nil != err {
		util.LogErrorf("download request [%s] failed: %s", downloadURL, err)
		return
	}
	if 200 != resp.StatusCode {
		util.LogErrorf("download request [%s] status code [%d]", downloadURL, resp.StatusCode)
		err = errors.New(fmt.Sprintf("download file list failed [%d]", resp.StatusCode))
		return
	}

	data, err := resp.ToBytes()
	if err = gulu.JSON.UnmarshalJSON(data, &ret); nil != err {
		util.LogErrorf("unmarshal index failed: %s", err)
		err = errors.New(fmt.Sprintf("unmarshal index failed"))
		return
	}
	return
}

func localUpsertRemoveListOSS(localDirPath string, cloudFileList map[string]*CloudIndex) (localRemoves, cloudFetches []string, err error) {
	unchanged := map[string]bool{}

	filepath.Walk(localDirPath, func(path string, info fs.FileInfo, err error) error {
		if localDirPath == path {
			return nil
		}

		if info.IsDir() {
			return nil
		}

		relPath := filepath.ToSlash(strings.TrimPrefix(path, localDirPath))
		cloudIdx, ok := cloudFileList[relPath]
		if !ok {
			if util.CloudSingleFileMaxSizeLimit < info.Size() {
				util.LogWarnf("file [%s] larger than 100MB, ignore removing it", path)
				return nil
			}

			localRemoves = append(localRemoves, path)
			return nil
		}

		localModTime := info.ModTime().Unix()
		if cloudIdx.Updated == localModTime {
			unchanged[relPath] = true
		}
		return nil
	})

	for cloudPath, cloudIndex := range cloudFileList {
		if _, ok := unchanged[cloudPath]; ok {
			continue
		}
		if util.CloudSingleFileMaxSizeLimit < cloudIndex.Size {
			util.LogWarnf("cloud file [%s] larger than 100MB, ignore fetching it", cloudPath)
			continue
		}
		cloudFetches = append(cloudFetches, cloudPath)
	}
	return
}

func cloudUpsertRemoveLocalListOSS(localDirPath string, removedSyncList, upsertedSyncList, excludes map[string]bool) (localUpserts, cloudRemoves []string, err error) {
	localUpserts, cloudRemoves = []string{}, []string{}

	for removed, _ := range removedSyncList {
		cloudRemoves = append(cloudRemoves, removed)
	}

	for upsert, _ := range upsertedSyncList {
		p := filepath.Join(localDirPath, upsert)
		if excludes[p] {
			continue
		}
		info, statErr := os.Stat(p)
		if nil != statErr {
			util.LogErrorf("stat file [%s] failed: %s", p, statErr)
			err = statErr
			return
		}
		if util.CloudSingleFileMaxSizeLimit < info.Size() {
			util.LogWarnf("file [%s] larger than 100MB, ignore uploading it", p)
			continue
		}
		localUpserts = append(localUpserts, p)
	}
	return
}

func cloudUpsertRemoveListOSS(localDirPath string, cloudFileList, localFileList map[string]*CloudIndex, removeList, upsertList, excludes map[string]bool) (localUpserts, cloudRemoves []string, downloadList map[string]bool, err error) {
	localUpserts, cloudRemoves = []string{}, []string{}

	cloudChangedList := map[string]bool{}
	unchanged := map[string]bool{}
	for cloudFile, cloudIdx := range cloudFileList {
		localIdx := localFileList[cloudFile]
		if nil == localIdx {
			cloudRemoves = append(cloudRemoves, cloudFile)
			continue
		}
		if localIdx.Updated == cloudIdx.Updated {
			unchanged[filepath.Join(localDirPath, cloudFile)] = true
			continue
		}

		cloudChangedList[cloudFile] = true
	}

	delete(unchanged, filepath.Join(localDirPath, "index.json")) // 同步偶尔报错 `The system cannot find the path specified.` https://github.com/siyuan-note/siyuan/issues/4942
	filepath.Walk(localDirPath, func(path string, info fs.FileInfo, err error) error {
		if localDirPath == path || info.IsDir() {
			return nil
		}

		if !unchanged[path] {
			if excludes[path] {
				return nil
			}
			if util.CloudSingleFileMaxSizeLimit < info.Size() {
				util.LogWarnf("file [%s] larger than 100MB, ignore uploading it", path)
				return nil
			}
			localUpserts = append(localUpserts, path)
			return nil
		}
		return nil
	})

	downloadList = map[string]bool{}
	for cloudChanged, _ := range cloudChangedList {
		if upsertList[cloudChanged] || removeList[cloudChanged] || excludes[cloudChanged] {
			continue
		}
		downloadList[cloudChanged] = true
	}
	return
}

func putFileToCloud(filePath, key, upToken string) (err error) {
	formUploader := storage.NewFormUploader(&storage.Config{UseHTTPS: true})
	ret := storage.PutRet{}
	err = formUploader.PutFile(context.Background(), &ret, upToken, key, filePath, nil)
	if nil != err {
		util.LogWarnf("put file [%s] to cloud failed [%s], retry it after 3s", filePath, err)
		time.Sleep(3 * time.Second)
		err = formUploader.PutFile(context.Background(), &ret, upToken, key, filePath, nil)
		if nil != err {
			return
		}
		util.LogInfof("put file [%s] to cloud retry success", filePath)
	}
	return
}
