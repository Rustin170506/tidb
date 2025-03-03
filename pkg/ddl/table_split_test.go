// Copyright 2017 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ddl_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/ddl"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/session"
	"github.com/pingcap/tidb/pkg/store/mockstore"
	"github.com/pingcap/tidb/pkg/tablecodec"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
	"github.com/tikv/client-go/v2/tikv"
)

func TestTableSplit(t *testing.T) {
	store, err := mockstore.NewMockStore(mockstore.WithStoreType(mockstore.EmbedUnistore))
	require.NoError(t, err)
	defer func() {
		err := store.Close()
		require.NoError(t, err)
	}()
	session.SetSchemaLease(100 * time.Millisecond)
	session.DisableStats4Test()
	atomic.StoreUint32(&ddl.EnableSplitTableRegion, 1)
	dom, err := session.BootstrapSession(store)
	require.NoError(t, err)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	// Synced split table region.
	tk.MustExec("set @@session.tidb_scatter_region = 'table'")
	tk.MustExec(`create table t_part (a int key) partition by range(a) (
		partition p0 values less than (10),
		partition p1 values less than (20)
	)`)
	tk.MustQuery("select @@global.tidb_scatter_region;").Check(testkit.Rows(""))
	tk.MustExec("set @@global.tidb_scatter_region = 'table'")
	tk = testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec(`create table t_part_2 (a int key) partition by range(a) (
		partition p0 values less than (10),
		partition p1 values less than (20)
	)`)
	defer dom.Close()
	atomic.StoreUint32(&ddl.EnableSplitTableRegion, 0)
	infoSchema := dom.InfoSchema()
	require.NotNil(t, infoSchema)
	tbl, err := infoSchema.TableByName(context.Background(), ast.NewCIStr("mysql"), ast.NewCIStr("tidb"))
	require.NoError(t, err)
	checkRegionStartWithTableID(t, tbl.Meta().ID, store.(kvStore))

	tbl, err = infoSchema.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t_part"))
	require.NoError(t, err)
	pi := tbl.Meta().GetPartitionInfo()
	require.NotNil(t, pi)
	for _, def := range pi.Definitions {
		checkRegionStartWithTableID(t, def.ID, store.(kvStore))
	}
	tbl, err = infoSchema.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t_part_2"))
	require.NoError(t, err)
	pi = tbl.Meta().GetPartitionInfo()
	require.NotNil(t, pi)
	for _, def := range pi.Definitions {
		checkRegionStartWithTableID(t, def.ID, store.(kvStore))
	}
}

// TestScatterRegion test the behavior of the tidb_scatter_region system variable, for verifying:
// 1. The variable can be set and queried correctly at both session and global levels.
// 2. Changes to the global variable affect new sessions but not existing ones.
// 3. The variable only accepts valid values (”, 'table', 'global').
// 4. Attempts to set invalid values result in appropriate error messages.
func TestScatterRegion(t *testing.T) {
	store := testkit.CreateMockStore(t)
	tk := testkit.NewTestKit(t, store)
	tk2 := testkit.NewTestKit(t, store)

	tk.MustQuery("select @@tidb_scatter_region;").Check(testkit.Rows(""))
	tk.MustExec("set @@tidb_scatter_region = 'table';")
	tk.MustQuery("select @@tidb_scatter_region;").Check(testkit.Rows("table"))
	tk.MustExec("set @@tidb_scatter_region = 'global';")
	tk.MustQuery("select @@tidb_scatter_region;").Check(testkit.Rows("global"))
	tk.MustExec("set @@tidb_scatter_region = 'TABLE';")
	tk.MustQuery("select @@tidb_scatter_region;").Check(testkit.Rows("table"))
	tk.MustExec("set @@tidb_scatter_region = 'GLOBAL';")
	tk.MustQuery("select @@tidb_scatter_region;").Check(testkit.Rows("global"))
	tk.MustExec("set @@tidb_scatter_region = '';")
	tk.MustQuery("select @@tidb_scatter_region;").Check(testkit.Rows(""))

	tk.MustExec("set global tidb_scatter_region = 'table';")
	tk.MustQuery("select @@global.tidb_scatter_region;").Check(testkit.Rows("table"))
	tk.MustQuery("select @@tidb_scatter_region;").Check(testkit.Rows(""))
	tk2.MustQuery("select @@tidb_scatter_region;").Check(testkit.Rows(""))
	tk2 = testkit.NewTestKit(t, store)
	tk2.MustQuery("select @@tidb_scatter_region;").Check(testkit.Rows("table"))

	tk.MustExec("set global tidb_scatter_region = 'global';")
	tk.MustQuery("select @@global.tidb_scatter_region;").Check(testkit.Rows("global"))
	tk.MustExec("set global tidb_scatter_region = '';")
	tk.MustQuery("select @@global.tidb_scatter_region;").Check(testkit.Rows(""))
	tk2 = testkit.NewTestKit(t, store)
	tk2.MustQuery("select @@tidb_scatter_region;").Check(testkit.Rows(""))

	tk.MustExec("set global tidb_scatter_region = 'TABLE';")
	tk.MustQuery("select @@global.tidb_scatter_region;").Check(testkit.Rows("table"))
	tk.MustQuery("select @@tidb_scatter_region;").Check(testkit.Rows(""))
	tk2 = testkit.NewTestKit(t, store)
	tk2.MustQuery("select @@tidb_scatter_region;").Check(testkit.Rows("table"))

	tk.MustExec("set global tidb_scatter_region = 'GLOBAL';")
	tk.MustQuery("select @@global.tidb_scatter_region;").Check(testkit.Rows("global"))
	tk.MustExec("set global tidb_scatter_region = '';")
	tk.MustQuery("select @@global.tidb_scatter_region;").Check(testkit.Rows(""))

	err := tk.ExecToErr("set @@tidb_scatter_region = 'test';")
	require.ErrorContains(t, err, "invalid value for 'test', it should be either '', 'table' or 'global'")
	err = tk.ExecToErr("set @@tidb_scatter_region = 'te st';")
	require.ErrorContains(t, err, "invalid value for 'te st', it should be either '', 'table' or 'global'")
	err = tk.ExecToErr("set @@tidb_scatter_region = '1';")
	require.ErrorContains(t, err, "invalid value for '1', it should be either '', 'table' or 'global'")
	err = tk.ExecToErr("set @@tidb_scatter_region = 0;")
	require.ErrorContains(t, err, "invalid value for '0', it should be either '', 'table' or 'global'")

	tk.MustQuery("select @@tidb_scatter_region;").Check(testkit.Rows(""))
	tk.MustExec("set @@tidb_scatter_region = 'TaBlE';")
	tk.MustQuery("select @@tidb_scatter_region;").Check(testkit.Rows("table"))
	tk.MustExec("set @@tidb_scatter_region = 'gLoBaL';")
	tk.MustQuery("select @@tidb_scatter_region;").Check(testkit.Rows("global"))
}

type kvStore interface {
	GetRegionCache() *tikv.RegionCache
}

func checkRegionStartWithTableID(t *testing.T, id int64, store kvStore) {
	regionStartKey := tablecodec.EncodeTablePrefix(id)
	var loc *tikv.KeyLocation
	var err error
	cache := store.GetRegionCache()
	loc, err = cache.LocateKey(tikv.NewBackoffer(context.Background(), 5000), regionStartKey)
	require.NoError(t, err)
	// Region cache may be out of date, so we need to drop this expired region and load it again.
	cache.InvalidateCachedRegion(loc.Region)
	require.Equal(t, []byte(regionStartKey), loc.StartKey)
}
