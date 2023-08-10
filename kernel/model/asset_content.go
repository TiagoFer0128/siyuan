// SiYuan - Refactor your thinking
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
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"code.sajari.com/docconv"
	"github.com/88250/gulu"
	"github.com/88250/lute/ast"
	"github.com/siyuan-note/eventbus"
	"github.com/siyuan-note/filelock"
	"github.com/siyuan-note/logging"
	"github.com/siyuan-note/siyuan/kernel/sql"
	"github.com/siyuan-note/siyuan/kernel/task"
	"github.com/siyuan-note/siyuan/kernel/util"
	"github.com/xuri/excelize/v2"
)

// FullTextSearchAssetContent 搜索资源文件内容。
//
// method：0：关键字，1：查询语法，2：SQL，3：正则表达式
// orderBy: 0：相关度（默认），1：按更新时间升序，2：按更新时间降序
func FullTextSearchAssetContent(query string, types map[string]bool, method, orderBy, page, pageSize int) (ret []*Block, matchedBlockCount, matchedRootCount, pageCount int) {
	query = strings.TrimSpace(query)
	beforeLen := 36
	var blocks []*Block
	orderByClause := buildAssetContentOrderBy(orderBy)
	switch method {
	case 1: // 查询语法
		filter := buildAssetContentTypeFilter(types)
		blocks, matchedRootCount = fullTextSearchAssetContentByQuerySyntax(query, filter, orderByClause, beforeLen, page, pageSize)
	case 2: // SQL
		blocks, matchedRootCount = searchAssetContentBySQL(query, beforeLen, page, pageSize)
	case 3: // 正则表达式
		typeFilter := buildAssetContentTypeFilter(types)
		blocks, matchedRootCount = fullTextSearchAssetContentByRegexp(query, typeFilter, orderByClause, beforeLen, page, pageSize)
	default: // 关键字
		filter := buildAssetContentTypeFilter(types)
		blocks, matchedRootCount = fullTextSearchAssetContentByKeyword(query, filter, orderByClause, beforeLen, page, pageSize)
	}
	pageCount = (matchedRootCount + pageSize - 1) / pageSize

	if 1 > len(ret) {
		ret = []*Block{}
	}
	return
}

func fullTextSearchAssetContentByQuerySyntax(query, typeFilter, orderBy string, beforeLen, page, pageSize int) (ret []*Block, matchedAssetsCount int) {
	query = gulu.Str.RemoveInvisible(query)
	return fullTextSearchAssetContentByFTS(query, typeFilter, orderBy, beforeLen, page, pageSize)
}

func fullTextSearchAssetContentByKeyword(query, typeFilter string, orderBy string, beforeLen, page, pageSize int) (ret []*Block, matchedAssetsCount int) {
	query = gulu.Str.RemoveInvisible(query)
	query = stringQuery(query)
	return fullTextSearchAssetContentByFTS(query, typeFilter, orderBy, beforeLen, page, pageSize)
}

func fullTextSearchAssetContentByRegexp(exp, typeFilter, orderBy string, beforeLen, page, pageSize int) (ret []*Block, matchedAssetsCount int) {
	exp = gulu.Str.RemoveInvisible(exp)
	fieldFilter := assetContentFieldRegexp(exp)
	stmt := "SELECT * FROM `asset_contents_fts_case_insensitive` WHERE " + fieldFilter + " AND ext IN " + typeFilter
	stmt += " " + orderBy
	stmt += " LIMIT " + strconv.Itoa(pageSize) + " OFFSET " + strconv.Itoa((page-1)*pageSize)
	blocks := sql.SelectBlocksRawStmtNoParse(stmt, Conf.Search.Limit)
	ret = fromSQLBlocks(&blocks, "", beforeLen)
	if 1 > len(ret) {
		ret = []*Block{}
	}

	matchedAssetsCount = fullTextSearchAssetContentCountByRegexp(exp, typeFilter)
	return
}

func assetContentFieldRegexp(exp string) string {
	buf := bytes.Buffer{}
	buf.WriteString("(name REGEXP '")
	buf.WriteString(exp)
	buf.WriteString("' OR content REGEXP '")
	buf.WriteString(exp)
	buf.WriteString("')")
	return buf.String()
}

func fullTextSearchAssetContentCountByRegexp(exp, typeFilter string) (matchedAssetsCount int) {
	fieldFilter := fieldRegexp(exp)
	stmt := "SELECT COUNT(path) AS `assets` FROM `blocks` WHERE " + fieldFilter + " AND type IN " + typeFilter
	result, _ := sql.QueryNoLimit(stmt)
	if 1 > len(result) {
		return
	}
	matchedAssetsCount = int(result[0]["assets"].(int64))
	return
}

func fullTextSearchAssetContentByFTS(query, typeFilter, orderBy string, beforeLen, page, pageSize int) (ret []*Block, matchedAssetsCount int) {
	table := "asset_contents_fts_case_insensitive"
	projections := "id, name, ext, path, size, updated, " +
		"highlight(" + table + ", 6, '<mark>', '</mark>') AS content"
	stmt := "SELECT " + projections + " FROM " + table + " WHERE (`" + table + "` MATCH '" + buildAssetContentColumnFilter() + ":(" + query + ")'"
	stmt += ") AND type IN " + typeFilter
	stmt += " " + orderBy
	stmt += " LIMIT " + strconv.Itoa(pageSize) + " OFFSET " + strconv.Itoa((page-1)*pageSize)
	blocks := sql.SelectBlocksRawStmt(stmt, page, pageSize)
	ret = fromSQLBlocks(&blocks, "", beforeLen)
	if 1 > len(ret) {
		ret = []*Block{}
	}

	matchedAssetsCount = fullTextSearchAssetContentCount(query, typeFilter)
	return
}

func searchAssetContentBySQL(stmt string, beforeLen, page, pageSize int) (ret []*Block, matchedAssetsCount int) {
	stmt = gulu.Str.RemoveInvisible(stmt)
	stmt = strings.TrimSpace(stmt)
	blocks := sql.SelectBlocksRawStmt(stmt, page, pageSize)
	ret = fromSQLBlocks(&blocks, "", beforeLen)
	if 1 > len(ret) {
		ret = []*Block{}
		return
	}

	stmt = strings.ToLower(stmt)
	stmt = strings.ReplaceAll(stmt, "select * ", "select COUNT(path) AS `assets` ")
	stmt = removeLimitClause(stmt)
	result, _ := sql.QueryNoLimit(stmt)
	if 1 > len(ret) {
		return
	}

	matchedAssetsCount = int(result[0]["assets"].(int64))
	return
}

func fullTextSearchAssetContentCount(query, typeFilter string) (matchedAssetsCount int) {
	query = gulu.Str.RemoveInvisible(query)

	table := "asset_contents_fts_case_insensitive"
	stmt := "SELECT COUNT(path) AS `assets` FROM `" + table + "` WHERE (`" + table + "` MATCH '" + buildAssetContentColumnFilter() + ":(" + query + ")'"
	stmt += ") AND type IN " + typeFilter
	result, _ := sql.QueryNoLimit(stmt)
	if 1 > len(result) {
		return
	}
	matchedAssetsCount = int(result[0]["assets"].(int64))
	return
}

func buildAssetContentColumnFilter() string {
	return "{name content}"
}

func buildAssetContentTypeFilter(types map[string]bool) string {
	if 0 == len(types) {
		return ""
	}

	var buf bytes.Buffer
	buf.WriteString("(")
	for k, _ := range types {
		buf.WriteString("'")
		buf.WriteString(k)
		buf.WriteString("',")
	}
	buf.Truncate(buf.Len() - 1)
	buf.WriteString(")")
	return buf.String()
}

func buildAssetContentOrderBy(orderBy int) string {
	switch orderBy {
	case 0:
		return "ORDER BY rank DESC"
	case 1:
		return "ORDER BY updated ASC"
	case 2:
		return "ORDER BY updated DESC"
	default:
		return "ORDER BY rank DESC"
	}
}

var assetContentSearcher = NewAssetsSearcher()

func IndexAssetContent(absPath string) {
	assetsDir := util.GetDataAssetsAbsPath()

	ext := strings.ToLower(filepath.Ext(absPath))
	parser, found := assetContentSearcher.Parsers[ext]
	if !found {
		return
	}

	result := parser.Parse(absPath)
	if nil == result {
		return
	}

	info, err := os.Stat(absPath)
	if nil != err {
		logging.LogErrorf("stat [%s] failed: %s", absPath, err)
		return
	}

	p := "assets" + filepath.ToSlash(strings.TrimPrefix(absPath, assetsDir))

	assetContents := []*sql.AssetContent{
		{
			ID:      ast.NewNodeID(),
			Name:    util.RemoveID(filepath.Base(p)),
			Ext:     ext,
			Path:    p,
			Size:    info.Size(),
			Updated: info.ModTime().Unix(),
			Content: result.Content,
		},
	}

	sql.DeleteAssetContentsByPathQueue(p)
	sql.IndexAssetContentsQueue(assetContents)
}

func ReindexAssetContent() {
	task.AppendTask(task.AssetContentDatabaseIndexFull, fullReindexAssetContent)
	return
}

func fullReindexAssetContent() {
	util.PushMsg(Conf.Language(216), 7*1000)
	sql.InitAssetContentDatabase(true)

	assetContentSearcher.FullIndex()
	return
}

func init() {
	subscribeSQLAssetContentEvents()
}

func subscribeSQLAssetContentEvents() {
	eventbus.Subscribe(util.EvtSQLAssetContentRebuild, func() {
		ReindexAssetContent()
	})
}

var (
	AssetsSearchEnabled = true
)

type AssetsSearcher struct {
	Parsers map[string]AssetParser

	lock *sync.Mutex
}

func (searcher *AssetsSearcher) FullIndex() {
	assetsDir := util.GetDataAssetsAbsPath()
	if !gulu.File.IsDir(assetsDir) {
		return
	}

	var results []*AssetParseResult
	filepath.Walk(assetsDir, func(absPath string, info fs.FileInfo, err error) error {
		if nil != err {
			logging.LogErrorf("walk dir [%s] failed: %s", absPath, err)
			return err
		}

		if info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(absPath))
		parser, found := searcher.Parsers[ext]
		if !found {
			return nil
		}

		result := parser.Parse(absPath)
		if nil == result {
			return nil
		}

		result.Path = "assets" + filepath.ToSlash(strings.TrimPrefix(absPath, assetsDir))
		result.Size = info.Size()
		result.Updated = info.ModTime().Unix()
		results = append(results, result)
		return nil
	})

	var assetContents []*sql.AssetContent
	for _, result := range results {
		assetContents = append(assetContents, &sql.AssetContent{
			ID:      ast.NewNodeID(),
			Name:    util.RemoveID(filepath.Base(result.Path)),
			Ext:     strings.ToLower(filepath.Ext(result.Path)),
			Path:    result.Path,
			Size:    result.Size,
			Updated: result.Updated,
			Content: result.Content,
		})
	}

	sql.IndexAssetContentsQueue(assetContents)
}

func NewAssetsSearcher() *AssetsSearcher {
	return &AssetsSearcher{
		Parsers: map[string]AssetParser{
			".txt":      &TxtAssetParser{},
			".md":       &TxtAssetParser{},
			".markdown": &TxtAssetParser{},
			".docx":     &DocxAssetParser{},
			".pptx":     &PptxAssetParser{},
			".xlsx":     &XlsxAssetParser{},
		},

		lock: &sync.Mutex{},
	}
}

type AssetParseResult struct {
	Path    string
	Size    int64
	Updated int64
	Content string
}

type AssetParser interface {
	Parse(absPath string) *AssetParseResult
}

type TxtAssetParser struct {
}

func (parser *TxtAssetParser) Parse(absPath string) (ret *AssetParseResult) {
	if !strings.HasSuffix(strings.ToLower(absPath), ".txt") {
		return
	}

	data, err := filelock.ReadFile(absPath)
	if nil != err {
		logging.LogErrorf("read file [%s] failed: %s", absPath, err)
		return
	}

	content := normalizeAssetContent(string(data))
	ret = &AssetParseResult{
		Content: content,
	}
	return
}

func normalizeAssetContent(content string) (ret string) {
	ret = strings.Join(strings.Fields(content), " ")
	return
}

func copyTempAsset(absPath string) (ret string) {
	dir := filepath.Join(util.TempDir, "convert", "asset_content")
	if err := os.MkdirAll(dir, 0755); nil != err {
		logging.LogErrorf("mkdir [%s] failed: [%s]", dir, err)
		return
	}

	ret = filepath.Join(dir, gulu.Rand.String(7)+".docx")
	if err := filelock.Copy(absPath, ret); nil != err {
		logging.LogErrorf("copy [%s] to [%s] failed: [%s]", absPath, ret, err)
		return
	}
	return
}

type DocxAssetParser struct {
}

func (parser *DocxAssetParser) Parse(absPath string) (ret *AssetParseResult) {
	if !strings.HasSuffix(strings.ToLower(absPath), ".docx") {
		return
	}

	if !gulu.File.IsExist(absPath) {
		return
	}

	tmp := copyTempAsset(absPath)
	if "" == tmp {
		return
	}
	defer os.RemoveAll(tmp)

	f, err := os.Open(tmp)
	if nil != err {
		logging.LogErrorf("open [%s] failed: [%s]", tmp, err)
		return
	}
	defer f.Close()

	data, _, err := docconv.ConvertDocx(f)
	if nil != err {
		logging.LogErrorf("convert [%s] failed: [%s]", tmp, err)
		return
	}

	var content = normalizeAssetContent(data)
	ret = &AssetParseResult{
		Content: content,
	}
	return
}

type PptxAssetParser struct {
}

func (parser *PptxAssetParser) Parse(absPath string) (ret *AssetParseResult) {
	if !strings.HasSuffix(strings.ToLower(absPath), ".pptx") {
		return
	}

	if !gulu.File.IsExist(absPath) {
		return
	}

	tmp := copyTempAsset(absPath)
	if "" == tmp {
		return
	}
	defer os.RemoveAll(tmp)

	f, err := os.Open(tmp)
	if nil != err {
		logging.LogErrorf("open [%s] failed: [%s]", tmp, err)
		return
	}
	defer f.Close()

	data, _, err := docconv.ConvertPptx(f)
	if nil != err {
		logging.LogErrorf("convert [%s] failed: [%s]", tmp, err)
		return
	}

	var content = normalizeAssetContent(data)
	ret = &AssetParseResult{
		Content: content,
	}
	return
}

type XlsxAssetParser struct {
}

func (parser *XlsxAssetParser) Parse(absPath string) (ret *AssetParseResult) {
	if !strings.HasSuffix(strings.ToLower(absPath), ".xlsx") {
		return
	}

	if !gulu.File.IsExist(absPath) {
		return
	}

	tmp := copyTempAsset(absPath)
	if "" == tmp {
		return
	}
	defer os.RemoveAll(tmp)

	x, err := excelize.OpenFile(tmp)
	if nil != err {
		logging.LogErrorf("open [%s] failed: [%s]", tmp, err)
		return
	}
	defer x.Close()

	buf := bytes.Buffer{}
	sheetMap := x.GetSheetMap()
	for _, sheetName := range sheetMap {
		rows, getErr := x.GetRows(sheetName)
		if nil != getErr {
			logging.LogErrorf("get rows from sheet [%s] failed: [%s]", sheetName, getErr)
			return
		}
		for _, row := range rows {
			for _, colCell := range row {
				buf.WriteString(colCell + " ")
			}
		}
	}

	var content = normalizeAssetContent(buf.String())
	ret = &AssetParseResult{
		Content: content,
	}
	return
}
