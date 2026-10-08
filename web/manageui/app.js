/* global Vue, echarts, API */
// IOT-TSDB 测点管理单页应用：hash router + 六个视图。
(function () {
  const { createApp, reactive } = Vue;

  // ---------------- shared helpers ----------------

  const ROUTES = ['dashboard', 'points', 'realtime', 'history', 'io', 'backup'];

  const toastState = reactive({ msg: '', type: 'ok' });
  let toastTimer = null;
  function showToast(msg, type) {
    toastState.msg = msg;
    toastState.type = type || 'ok';
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { toastState.msg = ''; }, 3500);
  }

  function pad(n) { return String(n).padStart(2, '0'); }
  function fmtTime(ms) {
    if (!ms) return '-';
    const d = new Date(Number(ms));
    return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()) +
      ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds());
  }
  function fmtBytes(n) {
    if (n === undefined || n === null) return '-';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0, v = Number(n);
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
    return (i === 0 ? v : v.toFixed(1)) + ' ' + units[i];
  }
  function fmtRetention(ms) {
    if (!ms) return '-';
    const h = ms / 3600000;
    if (h % 24 === 0) return (h / 24) + ' d';
    if (h >= 1) return h + ' h';
    return Math.round(ms / 60000) + ' min';
  }
  function fmtVal(v) {
    if (v === undefined || v === null || Number.isNaN(v)) return '-';
    const a = Math.abs(v);
    if (a !== 0 && (a >= 1e9 || a < 1e-4)) return v.toExponential(3);
    return String(Math.round(v * 10000) / 10000);
  }
  function labelsText(p) {
    const ents = Object.entries(p.labels || {});
    if (!ents.length) return '';
    return '{' + ents.map(([k, v]) => k + '="' + v + '"').join(', ') + '}';
  }
  function displayName(p) { return p.description || p.metric; }
  function keyToMatchers(p) {
    const ms = [{ name: '__name__', op: '=', value: p.metric }];
    for (const k of Object.keys(p.labels || {})) ms.push({ name: k, op: '=', value: p.labels[k] });
    return JSON.stringify(ms);
  }
  function csvCell(s) {
    s = String(s === undefined || s === null ? '' : s);
    return /[",\r\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
  }
  function downloadText(filename, text, mime) {
    // Prepend BOM so Excel renders Chinese correctly.
    const blob = new Blob(['\uFEFF' + text], { type: (mime || 'text/plain') + ';charset=utf-8' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = filename;
    a.click();
    URL.revokeObjectURL(a.href);
  }
  // Minimal CSV parser (handles quotes/embedded commas) for import preview.
  function parseCSVText(text) {
    text = text.replace(/^\uFEFF/, '');
    const rows = [];
    let row = [], field = '', inQ = false;
    for (let i = 0; i < text.length; i++) {
      const c = text[i];
      if (inQ) {
        if (c === '"') {
          if (text[i + 1] === '"') { field += '"'; i++; } else { inQ = false; }
        } else { field += c; }
      } else if (c === '"') { inQ = true; }
      else if (c === ',') { row.push(field); field = ''; }
      else if (c === '\n') { row.push(field); rows.push(row); row = []; field = ''; }
      else if (c !== '\r') { field += c; }
    }
    if (field !== '' || row.length) { row.push(field); rows.push(row); }
    return rows;
  }
  // Candidate-point loader shared by realtime/history pick lists; keeps a
  // meta cache so selected points survive search-filter reloads.
  function pointPicker(data) {
    return {
      candidates: [],
      pickSearch: '',
      selected: [],
      metaCache: {},
      _pickTimer: null,
      ...data,
    };
  }
  const pickerMethods = {
    async loadCandidates() {
      try {
        const resp = await API.points({ search: this.pickSearch, size: 100, page: 1 });
        this.candidates = resp.items || [];
        for (const p of this.candidates) this.metaCache[p.key] = p;
      } catch (e) { showToast(e.message, 'err'); }
    },
    onPickSearch() {
      clearTimeout(this._pickTimer);
      this._pickTimer = setTimeout(() => this.loadCandidates(), 300);
    },
    isSelected(key) { return this.selected.indexOf(key) >= 0; },
    togglePick(key) {
      const i = this.selected.indexOf(key);
      if (i >= 0) this.selected.splice(i, 1);
      else {
        const p = this.candidates.find((c) => c.key === key);
        if (p) this.metaCache[key] = p;
        this.selected.push(key);
      }
    },
  };
  function makeChart(el) {
    if (typeof echarts === 'undefined') {
      el.innerHTML = '<p class="dim" style="padding:20px">echarts 未加载（vendor/echarts.min.js 缺失）</p>';
      return null;
    }
    return echarts.init(el, null, { renderer: 'canvas' });
  }
  const CHART_TEXT = '#d7dde4';
  const CHART_DIM = '#8b95a1';
  const CHART_BORDER = '#333b47';

  // ---------------- root app ----------------

  const app = createApp({
    data() {
      return { route: 'dashboard', toast: toastState };
    },
    computed: {
      view() { return 'view-' + this.route; },
    },
    methods: {
      syncRoute() {
        const h = location.hash.replace(/^#\//, '').replace(/\/.*$/, '');
        this.route = ROUTES.indexOf(h) >= 0 ? h : 'dashboard';
      },
    },
    mounted() {
      this.syncRoute();
      window.addEventListener('hashchange', this.syncRoute);
    },
  });

  app.config.errorHandler = (err) => {
    showToast(err && err.message ? err.message : String(err), 'err');
  };

  // ---------------- view 1: dashboard ----------------

  app.component('view-dashboard', {
    template: `
<div>
  <h2>总览</h2>
  <div class="cards" v-if="d">
    <div class="card"><div class="label">时序数 (series)</div><div class="value">{{ d.series_count ?? '-' }}</div><div class="sub">Head 中活跃序列</div></div>
    <div class="card"><div class="label">元数据测点</div><div class="value">{{ d.meta_count }}</div><div class="sub">含预登记离线点</div></div>
    <div class="card"><div class="label">存储占用</div><div class="value">{{ fmtBytes(d.db_size_bytes) }}</div><div class="sub">数据目录总大小</div></div>
    <div class="card"><div class="label">备份</div><div class="value">{{ d.backups_count ?? 0 }}</div><div class="sub">zip 文件数</div></div>
    <div class="card"><div class="label">保留期</div><div class="value">{{ fmtRetention(d.retention_ms) }}</div><div class="sub">超期自动清理</div></div>
    <div class="card"><div class="label">最早数据</div><div class="value" style="font-size:15px;line-height:34px">{{ oldestText }}</div><div class="sub">最早持久块起始</div></div>
  </div>
  <div class="two-col" style="margin-top:16px">
    <div class="panel">
      <h3>分组分布（元数据）</h3>
      <div ref="pie" style="width:100%;height:280px"></div>
    </div>
    <div>
      <div class="panel">
        <h3>Head 统计</h3>
        <table v-if="hs">
          <tr><th>指标</th><th>值</th></tr>
          <tr><td>NumSeries</td><td class="mono">{{ hs.NumSeries ?? '-' }}</td></tr>
          <tr><td>ChunkCount</td><td class="mono">{{ hs.ChunkCount ?? '-' }}</td></tr>
          <tr><td>时间范围</td><td class="mono">{{ headRange }}</td></tr>
        </table>
        <p v-else class="dim">加载中…</p>
      </div>
      <div class="panel">
        <h3>入口</h3>
        <p><a href="/graph" target="_blank" style="color:var(--accent2)">原生 Prometheus UI（PromQL）→</a></p>
        <p style="margin-top:6px"><a href="/tsdb-status" target="_blank" style="color:var(--accent2)">TSDB 状态 →</a></p>
      </div>
    </div>
  </div>
</div>`,
    data() {
      return { d: null, chart: null, _resize: null };
    },
    computed: {
      hs() { return this.d ? this.d.head_stats : null; },
      oldestText() {
        return this.d && this.d.oldest_block_t ? fmtTime(this.d.oldest_block_t) : '-';
      },
      headRange() {
        if (!this.hs) return '-';
        return fmtTime(this.hs.MinTime) + ' ~ ' + fmtTime(this.hs.MaxTime);
      },
    },
    methods: {
      fmtBytes, fmtRetention, fmtTime,
      async load() {
        try {
          this.d = await API.dashboard();
          this.$nextTick(() => this.renderPie());
        } catch (e) { showToast(e.message, 'err'); }
      },
      renderPie() {
        if (!this.chart) return;
        const counts = (this.d && this.d.group_counts) || {};
        const data = Object.keys(counts).sort().map((g) => ({ name: g, value: counts[g] }));
        this.chart.setOption({
          backgroundColor: 'transparent',
          textStyle: { color: CHART_TEXT },
          tooltip: { backgroundColor: '#262c36', borderColor: CHART_BORDER, textStyle: { color: CHART_TEXT } },
          legend: { bottom: 0, textStyle: { color: CHART_DIM }, type: 'scroll' },
          series: [{
            type: 'pie', radius: ['35%', '65%'], center: ['50%', '45%'],
            label: { color: CHART_TEXT },
            data: data.length ? data : [{ name: '暂无元数据', value: 1, itemStyle: { color: '#333b47' } }],
          }],
        }, true);
      },
    },
    mounted() {
      if (this.$refs.pie) this.chart = makeChart(this.$refs.pie);
      this._resize = () => { if (this.chart) this.chart.resize(); };
      window.addEventListener('resize', this._resize);
      this.load();
    },
    beforeUnmount() {
      window.removeEventListener('resize', this._resize);
      if (this.chart) this.chart.dispose();
    },
  });

  // ---------------- view 2: points ----------------

  app.component('view-points', {
    template: `
<div>
  <h2>测点管理</h2>
  <div class="toolbar">
    <input v-model="search" placeholder="搜索 key / 描述" style="width:260px" @keyup.enter="reload(1)">
    <select v-model="group" @change="reload(1)">
      <option value="">全部分组</option>
      <option v-for="g in groups" :key="g" :value="g">{{ g }}</option>
    </select>
    <button class="primary" @click="reload(1)">查询</button>
    <span class="dim">共 {{ total }} 个测点</span>
  </div>
  <div class="panel" style="padding:0;overflow:auto">
    <table>
      <thead><tr>
        <th>状态</th><th>测点</th><th>描述</th><th>单位</th><th>量程</th><th>分组</th><th>最近值</th><th>最近时间</th><th></th>
      </tr></thead>
      <tbody>
        <tr v-for="p in items" :key="p.key">
          <td><span :class="['tag', p.online ? 'on' : 'off']">{{ p.online ? '在线' : '离线' }}</span></td>
          <td><div>{{ p.metric }}</div><div class="mono dim">{{ labelsText(p) }}</div></td>
          <td>{{ p.description || '-' }}</td>
          <td>{{ p.unit || '-' }}</td>
          <td class="mono">{{ rangeText(p) }}</td>
          <td><span v-if="p.group" class="tag">{{ p.group }}</span><span v-else class="dim">-</span></td>
          <td class="mono">{{ p.last_v === null || p.last_v === undefined ? '-' : fmtVal(p.last_v) }}</td>
          <td class="mono dim">{{ p.last_t ? fmtTime(p.last_t) : '-' }}</td>
          <td style="white-space:nowrap">
            <button @click="openEdit(p)">编辑</button>
            <button class="danger" @click="openDelete(p)" style="margin-left:6px">删除</button>
          </td>
        </tr>
        <tr v-if="!items.length"><td colspan="9" class="dim" style="text-align:center;padding:24px">无匹配测点</td></tr>
      </tbody>
    </table>
  </div>
  <div class="pager">
    <button :disabled="page<=1" @click="reload(page-1)">上一页</button>
    <span class="dim">{{ page }} / {{ totalPages }}</span>
    <button :disabled="page>=totalPages" @click="reload(page+1)">下一页</button>
    <select v-model="size" @change="reload(1)">
      <option :value="20">20 / 页</option>
      <option :value="50">50 / 页</option>
      <option :value="100">100 / 页</option>
    </select>
  </div>

  <div v-if="editing" class="modal-mask" @click.self="editing=null">
    <div class="modal">
      <h3>编辑元数据</h3>
      <p class="mono dim" style="margin-bottom:12px;word-break:break-all">{{ editing.key }}</p>
      <div class="form-row"><label>描述</label><input v-model="editing.description"></div>
      <div class="form-row"><label>单位</label><input v-model="editing.unit" placeholder="如 ℃、MPa、kWh"></div>
      <div class="form-row"><label>量程下限</label><input v-model="editing.range_min" placeholder="留空不设置"></div>
      <div class="form-row"><label>量程上限</label><input v-model="editing.range_max" placeholder="留空不设置"></div>
      <div class="form-row"><label>分组</label><input v-model="editing.group" placeholder="如 锅炉房 / 1号产线"></div>
      <div class="modal-actions">
        <button @click="editing=null">取消</button>
        <button class="primary" @click="saveEdit">保存</button>
      </div>
    </div>
  </div>

  <div v-if="deleting" class="modal-mask" @click.self="deleting=null">
    <div class="modal">
      <h3>删除测点</h3>
      <p style="margin-bottom:8px">将删除该测点的<b>全部历史数据</b>，不可恢复：</p>
      <p class="mono dim" style="word-break:break-all">{{ deleting.key }}</p>
      <div class="form-row" style="margin-top:12px">
        <label style="width:auto"><input type="checkbox" v-model="deleting.drop_meta" style="margin-right:6px">同时删除元数据（勾选后该测点彻底移除）</label>
      </div>
      <div class="modal-actions">
        <button @click="deleting=null">取消</button>
        <button class="danger" @click="confirmDelete">确认删除</button>
      </div>
    </div>
  </div>
</div>`,
    data() {
      return {
        search: '', group: '', groups: [],
        page: 1, size: 20, total: 0, items: [],
        editing: null, deleting: null,
      };
    },
    computed: {
      totalPages() { return Math.max(1, Math.ceil(this.total / this.size)); },
    },
    methods: {
      labelsText, fmtTime, fmtVal,
      rangeText(p) {
        if (p.range_min === null && p.range_max === null) return '-';
        if (p.range_min === undefined && p.range_max === undefined) return '-';
        const lo = p.range_min === null || p.range_min === undefined ? '' : fmtVal(p.range_min);
        const hi = p.range_max === null || p.range_max === undefined ? '' : fmtVal(p.range_max);
        if (!lo && !hi) return '-';
        return lo + ' ~ ' + hi;
      },
      async reload(page) {
        this.page = page || this.page;
        try {
          const resp = await API.points({ search: this.search, group: this.group, page: this.page, size: this.size });
          this.items = resp.items || [];
          this.total = resp.total || 0;
        } catch (e) { showToast(e.message, 'err'); }
        try {
          const d = await API.dashboard();
          this.groups = d.groups || [];
        } catch (e) { /* groups filter is optional */ }
      },
      openEdit(p) {
        this.editing = {
          key: p.key,
          description: p.description || '',
          unit: p.unit || '',
          range_min: p.range_min === null || p.range_min === undefined ? '' : String(p.range_min),
          range_max: p.range_max === null || p.range_max === undefined ? '' : String(p.range_max),
          group: p.group || '',
        };
      },
      async saveEdit() {
        const e = this.editing;
        const body = {
          key: e.key,
          description: e.description,
          unit: e.unit,
          group: e.group,
          range_min: e.range_min === '' ? null : Number(e.range_min),
          range_max: e.range_max === '' ? null : Number(e.range_max),
        };
        if (body.range_min !== null && Number.isNaN(body.range_min)) { showToast('量程下限不是数字', 'err'); return; }
        if (body.range_max !== null && Number.isNaN(body.range_max)) { showToast('量程上限不是数字', 'err'); return; }
        try {
          await API.putMeta(body);
          showToast('已保存');
          this.editing = null;
          this.reload();
        } catch (err) { showToast(err.message, 'err'); }
      },
      openDelete(p) {
        this.deleting = { key: p.key, point: p, drop_meta: true };
      },
      async confirmDelete() {
        const d = this.deleting;
        try {
          await API.deletePoints({ matchers: keyToMatchers(d.point), drop_meta: d.drop_meta });
          showToast('已删除');
          this.deleting = null;
          this.reload(1);
        } catch (err) { showToast(err.message, 'err'); }
      },
    },
    mounted() { this.reload(1); },
  });

  // ---------------- view 3: realtime ----------------

  app.component('view-realtime', {
    template: `
<div>
  <h2>实时值</h2>
  <div class="two-col">
    <div>
      <div class="toolbar">
        <input v-model="pickSearch" placeholder="搜索测点" style="width:100%" @input="onPickSearch">
      </div>
      <div class="pick-list">
        <div v-for="p in candidates" :key="p.key" class="pick-item" @click="togglePick(p.key)">
          <input type="checkbox" :checked="isSelected(p.key)" @click.stop @change="togglePick(p.key)">
          <div>
            <div>{{ displayName(p) }}</div>
            <div class="mono dim" style="font-size:11px">{{ p.key }}</div>
          </div>
        </div>
        <div v-if="!candidates.length" class="dim" style="padding:16px;text-align:center">无匹配测点</div>
      </div>
      <p class="dim" style="margin-top:6px;font-size:12px">已选 {{ selected.length }} 个；候选列表最多 100 条，可用搜索缩小范围</p>
    </div>
    <div>
      <div class="rt-grid" v-if="selected.length">
        <div v-for="key in selected" :key="key" :class="['rt-card', cardClass(key)]">
          <div class="name">{{ metaOf(key) ? displayName(metaOf(key)) : key }}</div>
          <div class="mono dim" style="font-size:11px">{{ key }}</div>
          <div><span class="val">{{ valText(key) }}</span><span class="unit">{{ (metaOf(key) && metaOf(key).unit) || '' }}</span></div>
          <div class="time">{{ timeText(key) }}</div>
        </div>
      </div>
      <div v-else class="panel dim" style="text-align:center;padding:40px">从左侧勾选要监视的测点</div>
    </div>
  </div>
</div>`,
    data() {
      const saved = (() => { try { return JSON.parse(localStorage.getItem('rt.selected') || '[]'); } catch (e) { return []; } })();
      return pointPicker({ values: {}, timer: null, selected: saved });
    },
    methods: {
      ...pickerMethods,
      displayName,
      metaOf(key) { return this.metaCache[key]; },
      valOf(key) { return this.values[key]; },
      valText(key) {
        const v = this.valOf(key);
        return v ? fmtVal(v.v) : '-';
      },
      timeText(key) {
        const v = this.valOf(key);
        if (!v) return '无数据';
        const age = Math.round((Date.now() - v.t) / 1000);
        return fmtTime(v.t) + (age >= 0 ? '（' + age + 's 前）' : '');
      },
      cardClass(key) {
        const v = this.valOf(key);
        if (!v) return 'stale';
        const m = this.metaOf(key);
        if (m) {
          if (m.range_min !== null && m.range_min !== undefined && v.v < m.range_min) return 'alarm';
          if (m.range_max !== null && m.range_max !== undefined && v.v > m.range_max) return 'alarm';
        }
        if (Date.now() - v.t > 15000) return 'stale';
        return '';
      },
      async poll() {
        if (!this.selected.length) return;
        try {
          this.values = await API.pointsLast(this.selected);
        } catch (e) { /* transient; keep last values */ }
      },
    },
    watch: {
      selected: {
        deep: true,
        handler() {
          try { localStorage.setItem('rt.selected', JSON.stringify(this.selected)); } catch (e) {}
          this.poll();
        },
      },
    },
    mounted() {
      this.loadCandidates();
      this.poll();
      this.timer = setInterval(() => this.poll(), 2000);
    },
    beforeUnmount() { clearInterval(this.timer); },
  });

  // ---------------- view 4: history ----------------

  app.component('view-history', {
    template: `
<div>
  <h2>历史分析</h2>
  <div class="two-col">
    <div>
      <div class="toolbar">
        <input v-model="pickSearch" placeholder="搜索测点" style="width:100%" @input="onPickSearch">
      </div>
      <div class="pick-list">
        <div v-for="p in candidates" :key="p.key" class="pick-item" @click="togglePick(p.key)">
          <input type="checkbox" :checked="isSelected(p.key)" @click.stop @change="togglePick(p.key)">
          <div>
            <div>{{ displayName(p) }}</div>
            <div class="mono dim" style="font-size:11px">{{ p.key }}</div>
          </div>
        </div>
        <div v-if="!candidates.length" class="dim" style="padding:16px;text-align:center">无匹配测点</div>
      </div>
      <p class="dim" style="margin-top:6px;font-size:12px">已选 {{ selected.length }} 个（建议 ≤ 8 条曲线）</p>
    </div>
    <div>
      <div class="toolbar">
        <select v-model="range">
          <option value="15m">最近 15 分钟</option>
          <option value="1h">最近 1 小时</option>
          <option value="6h">最近 6 小时</option>
          <option value="24h">最近 24 小时</option>
          <option value="7d">最近 7 天</option>
          <option value="custom">自定义</option>
        </select>
        <template v-if="range==='custom'">
          <input type="datetime-local" v-model="customStart" step="1">
          <span class="dim">至</span>
          <input type="datetime-local" v-model="customEnd" step="1">
        </template>
        <select v-model="agg">
          <option value="raw">原始值</option>
          <option value="avg">平均值</option>
          <option value="max">最大值</option>
          <option value="min">最小值</option>
        </select>
        <input v-model="step" placeholder="步长 ms（空=自动）" style="width:130px">
        <button class="primary" @click="query" :disabled="loading">{{ loading ? '查询中…' : '查询' }}</button>
        <button @click="exportCSV" :disabled="!lastData">导出 CSV</button>
      </div>
      <div ref="chart" class="chart panel"></div>
    </div>
  </div>
</div>`,
    data() {
      return pointPicker({
        range: '1h', customStart: '', customEnd: '',
        agg: 'raw', step: '', loading: false,
        chart: null, lastData: null, _resize: null,
      });
    },
    methods: {
      ...pickerMethods,
      displayName,
      timeWindow() {
        const now = Date.now();
        if (this.range !== 'custom') {
          const spans = { '15m': 9e5, '1h': 36e5, '6h': 216e5, '24h': 864e5, '7d': 6048e5 };
          return [now - spans[this.range], now];
        }
        if (!this.customStart || !this.customEnd) throw new Error('请选择自定义起止时间');
        const s = new Date(this.customStart).getTime();
        const e = new Date(this.customEnd).getTime();
        if (!(e > s)) throw new Error('结束时间必须大于开始时间');
        return [s, e];
      },
      async query() {
        if (!this.selected.length) { showToast('请先勾选测点', 'err'); return; }
        let start, end;
        try { [start, end] = this.timeWindow(); } catch (e) { showToast(e.message, 'err'); return; }
        this.loading = true;
        try {
          const params = { keys: this.selected.join(','), start, end, agg: this.agg };
          if (this.step !== '') {
            const st = Number(this.step);
            if (!Number.isFinite(st) || st < 1) { showToast('步长必须是正整数毫秒', 'err'); this.loading = false; return; }
            params.step = st;
          }
          this.lastData = await API.history(params);
          this.renderChart();
        } catch (e) { showToast(e.message, 'err'); }
        this.loading = false;
      },
      unitOf(key) {
        const m = this.metaCache[key];
        return (m && m.unit) || '';
      },
      nameOf(key) {
        const m = this.metaCache[key];
        return m ? displayName(m) : key;
      },
      renderChart() {
        if (!this.chart) return;
        const data = this.lastData;
        if (!data) return;
        // Split y axes by unit: first unit left, second right, extras share left.
        const units = [];
        for (const s of data.series) {
          const u = this.unitOf(s.key);
          if (units.indexOf(u) < 0) units.push(u);
        }
        const axisIndex = (u) => Math.min(units.indexOf(u), 1);
        const yAxis = units.slice(0, 2).map((u, i) => ({
          type: 'value', name: u, position: i === 0 ? 'left' : 'right', scale: true,
          nameTextStyle: { color: CHART_DIM },
          axisLine: { lineStyle: { color: CHART_BORDER } },
          splitLine: { lineStyle: { color: '#262c36' } },
        }));
        const series = data.series.map((s) => ({
          name: this.nameOf(s.key),
          type: 'line', showSymbol: false, connectNulls: false,
          yAxisIndex: axisIndex(this.unitOf(s.key)),
          data: (s.points || []).map((pt) => [pt[0], pt[1]]),
        }));
        this.chart.setOption({
          backgroundColor: 'transparent',
          textStyle: { color: CHART_TEXT },
          tooltip: {
            trigger: 'axis', backgroundColor: '#262c36',
            borderColor: CHART_BORDER, textStyle: { color: CHART_TEXT },
          },
          legend: { type: 'scroll', textStyle: { color: CHART_DIM } },
          grid: { left: 60, right: units.length > 1 ? 60 : 24, top: 42, bottom: 56 },
          xAxis: {
            type: 'time',
            axisLine: { lineStyle: { color: CHART_BORDER } },
            splitLine: { show: false },
          },
          yAxis: yAxis.length ? yAxis : [{ type: 'value' }],
          dataZoom: [{ type: 'inside' }, { type: 'slider', textStyle: { color: CHART_DIM } }],
          series,
        }, true);
      },
      exportCSV() {
        const data = this.lastData;
        if (!data) return;
        const keys = data.series.map((s) => s.key);
        const header = ['time'].concat(keys.map((k) => this.nameOf(k) + '(' + this.unitOf(k) + ')'));
        const byT = new Map();
        data.series.forEach((s, si) => {
          for (const pt of (s.points || [])) {
            if (!byT.has(pt[0])) byT.set(pt[0], new Array(keys.length).fill(''));
            byT.get(pt[0])[si] = pt[1];
          }
        });
        const ts = Array.from(byT.keys()).sort((a, b) => a - b);
        const lines = [header.map(csvCell).join(',')];
        for (const t of ts) {
          lines.push([csvCell(fmtTime(t))].concat(byT.get(t).map((x) => x === '' ? '' : String(x))).join(','));
        }
        downloadText('history.csv', lines.join('\r\n'), 'text/csv');
      },
    },
    mounted() {
      this.chart = makeChart(this.$refs.chart);
      this._resize = () => { if (this.chart) this.chart.resize(); };
      window.addEventListener('resize', this._resize);
      this.loadCandidates();
    },
    beforeUnmount() {
      window.removeEventListener('resize', this._resize);
      if (this.chart) this.chart.dispose();
    },
  });

  // ---------------- view 5: io (import/export) ----------------

  app.component('view-io', {
    template: `
<div>
  <h2>点表导入导出</h2>
  <div class="panel">
    <h3 style="margin-top:0">导出</h3>
    <p class="dim" style="margin-bottom:10px">导出全量点表（元数据 + 已发现 series）。CSV 列：metric, labels(k=v;k=v), description, unit, range_min, range_max, group。</p>
    <a :href="csvUrl" download><button class="primary">导出 CSV</button></a>
    <a :href="jsonUrl" download style="margin-left:8px"><button>导出 JSON</button></a>
  </div>
  <div class="panel">
    <h3 style="margin-top:0">导入</h3>
    <p class="dim" style="margin-bottom:10px">选择 CSV / JSON 点表文件，按 metric+labels 匹配 upsert（允许预登记尚无数据的测点）。</p>
    <div class="drop-zone" @click="$refs.file.click()">
      {{ file ? file.name : '点击选择 .csv / .json 文件' }}
      <input ref="file" type="file" accept=".csv,.json" style="display:none" @change="onFile">
    </div>
    <template v-if="preview.length">
      <h3>预览（前 {{ Math.min(preview.length, 20) }} 行 / 共 {{ previewTotal }} 行）</h3>
      <div style="overflow:auto;max-height:300px">
        <table>
          <thead><tr><th>metric</th><th>labels</th><th>description</th><th>unit</th><th>range_min</th><th>range_max</th><th>group</th></tr></thead>
          <tbody>
            <tr v-for="(row, i) in preview.slice(0, 20)" :key="i">
              <td class="mono">{{ row[0] }}</td><td class="mono">{{ row[1] }}</td><td>{{ row[2] }}</td>
              <td>{{ row[3] }}</td><td class="mono">{{ row[4] }}</td><td class="mono">{{ row[5] }}</td><td>{{ row[6] }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="modal-actions" style="justify-content:flex-start">
        <button class="primary" @click="doImport" :disabled="importing">{{ importing ? '导入中…' : '确认导入' }}</button>
        <button @click="reset">重新选择</button>
      </div>
    </template>
    <template v-if="result">
      <h3>导入结果</h3>
      <p>新建 <b style="color:var(--ok)">{{ result.created }}</b> 条，更新 <b style="color:var(--accent2)">{{ result.updated }}</b> 条，错误 <b :style="{color: result.errors && result.errors.length ? 'var(--err)' : 'inherit'}">{{ (result.errors || []).length }}</b> 条</p>
      <pre v-if="result.errors && result.errors.length" class="result">{{ result.errors.join('\\n') }}</pre>
    </template>
  </div>
</div>`,
    data() {
      return {
        csvUrl: API.exportUrl('csv'),
        jsonUrl: API.exportUrl('json'),
        file: null, preview: [], previewTotal: 0,
        importing: false, result: null,
      };
    },
    methods: {
      onFile(ev) {
        const f = ev.target.files && ev.target.files[0];
        if (!f) return;
        this.file = f;
        this.result = null;
        const reader = new FileReader();
        reader.onload = () => {
          try {
            if (/\.json$/i.test(f.name)) {
              const payload = JSON.parse(String(reader.result));
              const pts = payload.points || [];
              this.preview = pts.map((p) => [p.metric, p.labels, p.description, p.unit,
                p.range_min === null || p.range_min === undefined ? '' : p.range_min,
                p.range_max === null || p.range_max === undefined ? '' : p.range_max, p.group]);
            } else {
              this.preview = parseCSVText(String(reader.result))
                .filter((r) => r.length && String(r[0]).trim() !== '');
            }
            this.previewTotal = this.preview.length;
            if (!this.previewTotal) showToast('未解析到任何数据行', 'err');
          } catch (e) { showToast('解析失败：' + e.message, 'err'); this.reset(); }
        };
        reader.readAsText(f, 'utf-8');
      },
      reset() {
        this.file = null;
        this.preview = [];
        this.previewTotal = 0;
        this.$refs.file.value = '';
      },
      async doImport() {
        if (!this.file) return;
        this.importing = true;
        try {
          this.result = await API.importFile(this.file);
          showToast('导入完成：新建 ' + this.result.created + '，更新 ' + this.result.updated);
        } catch (e) { showToast(e.message, 'err'); }
        this.importing = false;
      },
    },
  });

  // ---------------- view 6: backup ----------------

  app.component('view-backup', {
    template: `
<div>
  <h2>备份还原</h2>
  <div class="panel">
    <h3 style="margin-top:0">创建备份</h3>
    <div class="toolbar" style="margin-bottom:0">
      <input v-model="newName" placeholder="备份名（留空自动命名，字母数字-_）" style="width:280px">
      <button class="primary" @click="create" :disabled="creating">{{ creating ? '备份中…' : '立即备份' }}</button>
    </div>
  </div>
  <div class="panel">
    <h3 style="margin-top:0">备份列表</h3>
    <table>
      <thead><tr><th>名称</th><th>大小</th><th>创建时间</th><th></th></tr></thead>
      <tbody>
        <tr v-for="b in list" :key="b.name">
          <td class="mono">{{ b.name }}</td>
          <td class="mono">{{ fmtBytes(b.size) }}</td>
          <td class="mono">{{ fmtTime(b.created) }}</td>
          <td style="white-space:nowrap">
            <a :href="downloadUrl(b.name)" download><button>下载</button></a>
            <button @click="confirmRestore = {name: b.name}" style="margin-left:6px">还原</button>
            <button class="danger" @click="confirmDelete = b.name" style="margin-left:6px">删除</button>
          </td>
        </tr>
        <tr v-if="!list.length"><td colspan="4" class="dim" style="text-align:center;padding:24px">暂无备份</td></tr>
      </tbody>
    </table>
  </div>
  <div class="panel">
    <h3 style="margin-top:0">上传 zip 还原</h3>
    <div class="drop-zone" @click="$refs.zip.click()">
      {{ uploadFile ? uploadFile.name : '点击选择备份 .zip 文件' }}
      <input ref="zip" type="file" accept=".zip" style="display:none" @change="onZip">
    </div>
  </div>

  <div v-if="confirmRestore" class="modal-mask" @click.self="confirmRestore=null">
    <div class="modal">
      <h3>确认还原</h3>
      <p style="margin-bottom:8px">将从 <b class="mono">{{ confirmRestore.name }}</b> 还原全部数据：</p>
      <ul style="margin:0 0 12px 18px;color:var(--fg-dim);font-size:13px">
        <li>当前数据库将被<b style="color:var(--err)">整体替换</b>（自动保留一份回滚副本）</li>
        <li>还原期间写入/查询会<b>短暂中断</b>（秒级），服务不重启</li>
        <li>zip 内点表元数据将覆盖当前元数据</li>
      </ul>
      <div class="modal-actions">
        <button @click="confirmRestore=null">取消</button>
        <button class="danger" @click="doRestore" :disabled="restoring">{{ restoring ? '还原中…' : '确认还原' }}</button>
      </div>
    </div>
  </div>

  <div v-if="confirmDelete" class="modal-mask" @click.self="confirmDelete=null">
    <div class="modal">
      <h3>删除备份</h3>
      <p>确定删除备份 <b class="mono">{{ confirmDelete }}</b>.zip ？</p>
      <div class="modal-actions">
        <button @click="confirmDelete=null">取消</button>
        <button class="danger" @click="doDelete">删除</button>
      </div>
    </div>
  </div>
</div>`,
    data() {
      return {
        list: [], newName: '', creating: false,
        confirmRestore: null, confirmDelete: null,
        uploadFile: null, restoring: false,
      };
    },
    methods: {
      fmtBytes, fmtTime,
      downloadUrl(name) { return API.backupDownloadUrl(name); },
      async load() {
        try {
          const resp = await API.backups();
          this.list = resp.backups || [];
        } catch (e) { showToast(e.message, 'err'); }
      },
      async create() {
        this.creating = true;
        try {
          const info = await API.backupCreate(this.newName.trim());
          showToast('备份完成：' + info.name);
          this.newName = '';
          this.load();
        } catch (e) { showToast(e.message, 'err'); }
        this.creating = false;
      },
      onZip(ev) {
        const f = ev.target.files && ev.target.files[0];
        if (!f) return;
        this.uploadFile = f;
        this.confirmRestore = { name: f.name, file: f };
        this.$refs.zip.value = '';
      },
      async doRestore() {
        this.restoring = true;
        try {
          if (this.confirmRestore.file) {
            await API.restoreUpload(this.confirmRestore.file);
          } else {
            await API.restoreByName(this.confirmRestore.name);
          }
          showToast('还原完成，数据已回退');
          this.confirmRestore = null;
          this.uploadFile = null;
        } catch (e) { showToast('还原失败：' + e.message, 'err'); }
        this.restoring = false;
      },
      async doDelete() {
        try {
          await API.backupDelete(this.confirmDelete);
          showToast('已删除');
          this.confirmDelete = null;
          this.load();
        } catch (e) { showToast(e.message, 'err'); }
      },
    },
    mounted() { this.load(); },
  });

  app.mount('#layout');
})();
