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
	"fmt"
	"strings"
	"time"

	"github.com/88250/lute/parse"
	"github.com/dustin/go-humanize"
	"github.com/emirpasic/gods/sets/hashset"
	"github.com/siyuan-note/eventbus"
	"github.com/siyuan-note/logging"
	"github.com/siyuan-note/siyuan/kernel/cache"
	"github.com/siyuan-note/siyuan/kernel/filesys"
	"github.com/siyuan-note/siyuan/kernel/sql"
	"github.com/siyuan-note/siyuan/kernel/task"
	"github.com/siyuan-note/siyuan/kernel/treenode"
	"github.com/siyuan-note/siyuan/kernel/util"
)

func (box *Box) Unindex() {
	task.PrependTask(task.DatabaseIndex, unindex, box.ID)
}

func unindex(boxID string) {
	ids := treenode.RemoveBlockTreesByBoxID(boxID)
	RemoveRecentDoc(ids)
	sql.DeleteBoxQueue(boxID)
}

func (box *Box) Index() {
	task.PrependTask(task.DatabaseIndex, index, box.ID)
	task.AppendTask(task.DatabaseIndexRef, IndexRefs)
}

func index(boxID string) {
	box := Conf.Box(boxID)
	if nil == box {
		return
	}

	util.SetBootDetails("Listing files...")
	files := box.ListFiles("/")
	boxLen := len(Conf.GetOpenedBoxes())
	if 1 > boxLen {
		boxLen = 1
	}
	bootProgressPart := 30.0 / float64(boxLen) / float64(len(files))

	start := time.Now()
	luteEngine := NewLute()
	var treeCount int
	var treeSize int64
	i := 0

	util.PushEndlessProgress(fmt.Sprintf("["+box.Name+"] "+Conf.Language(64), len(files)))
	for _, file := range files {
		if file.isdir || !strings.HasSuffix(file.name, ".sy") {
			continue
		}

		tree, err := filesys.LoadTree(box.ID, file.path, luteEngine)
		if nil != err {
			logging.LogErrorf("read box [%s] tree [%s] failed: %s", box.ID, file.path, err)
			continue
		}

		docIAL := parse.IAL2MapUnEsc(tree.Root.KramdownIAL)
		if "" == docIAL["updated"] { // 早期的数据可能没有 updated 属性，这里进行订正
			updated := util.TimeFromID(tree.Root.ID)
			tree.Root.SetIALAttr("updated", updated)
			docIAL["updated"] = updated
			if writeErr := filesys.WriteTree(tree); nil != writeErr {
				logging.LogErrorf("write tree [%s] failed: %s", tree.Path, writeErr)
			}
		}

		cache.PutDocIAL(file.path, docIAL)
		treenode.ReindexBlockTree(tree)
		sql.UpsertTreeQueue(tree)

		util.IncBootProgress(bootProgressPart, fmt.Sprintf(Conf.Language(92), util.ShortPathForBootingDisplay(tree.Path)))
		treeSize += file.size
		treeCount++
		if 1 < i && 0 == i%64 {
			util.PushEndlessProgress(fmt.Sprintf(Conf.Language(88), i, len(files)-i))
		}
		i++
	}

	box.UpdateHistoryGenerated() // 初始化历史生成时间为当前时间
	end := time.Now()
	elapsed := end.Sub(start).Seconds()
	logging.LogInfof("rebuilt database for notebook [%s] in [%.2fs], tree [count=%d, size=%s]", box.ID, elapsed, treeCount, humanize.Bytes(uint64(treeSize)))
	return
}

func IndexRefs() {
	sql.EnableCache()
	defer sql.ClearBlockCache()

	start := time.Now()
	util.SetBootDetails("Resolving refs...")
	util.PushEndlessProgress(Conf.Language(54))

	// 引用入库
	util.SetBootDetails("Indexing refs...")
	refBlocks := sql.GetRefExistedBlocks()
	refTreeIDs := hashset.New()
	for _, refBlock := range refBlocks {
		refTreeIDs.Add(refBlock.RootID)
	}

	if 0 < refTreeIDs.Size() {
		luteEngine := NewLute()
		bootProgressPart := 10.0 / float64(refTreeIDs.Size())
		for _, box := range Conf.GetOpenedBoxes() {
			sql.DeleteBoxRefsQueue(box.ID)

			files := box.ListFiles("/")
			i := 0
			for _, file := range files {
				if file.isdir || !strings.HasSuffix(file.name, ".sy") {
					continue
				}

				if file.isdir || !strings.HasSuffix(file.name, ".sy") {
					continue
				}

				id := strings.TrimSuffix(file.name, ".sy")
				if !refTreeIDs.Contains(id) {
					continue
				}

				util.IncBootProgress(bootProgressPart, "Indexing ref "+util.ShortPathForBootingDisplay(file.path))

				tree, err := filesys.LoadTree(box.ID, file.path, luteEngine)
				if nil != err {
					logging.LogErrorf("parse box [%s] tree [%s] failed", box.ID, file.path)
					continue
				}

				sql.InsertRefsTreeQueue(tree)
				if 1 < i && 0 == i%64 {
					util.PushEndlessProgress(fmt.Sprintf(Conf.Language(55), i))
				}
				i++
			}
		}
	}
	logging.LogInfof("resolved refs [%d] in [%dms]", len(refBlocks), time.Now().Sub(start).Milliseconds())
}

func init() {
	eventbus.Subscribe(eventbus.EvtSQLInsertBlocks, func(context map[string]interface{}, blockCount int, hash string) {
		if util.ContainerAndroid == util.Container || util.ContainerIOS == util.Container {
			// Android/iOS 端不显示数据索引和搜索索引状态提示 https://github.com/siyuan-note/siyuan/issues/6392
			return
		}

		msg := fmt.Sprintf(Conf.Language(89), blockCount, hash)
		util.SetBootDetails(msg)
		util.ContextPushMsg(context, msg)
	})
	eventbus.Subscribe(eventbus.EvtSQLInsertBlocksFTS, func(context map[string]interface{}, blockCount int, hash string) {
		if util.ContainerAndroid == util.Container || util.ContainerIOS == util.Container {
			// Android/iOS 端不显示数据索引和搜索索引状态提示 https://github.com/siyuan-note/siyuan/issues/6392
			return
		}

		msg := fmt.Sprintf(Conf.Language(90), blockCount, hash)
		util.SetBootDetails(msg)
		util.ContextPushMsg(context, msg)
	})
}
