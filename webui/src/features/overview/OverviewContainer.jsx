import { Activity, ArrowDown, ArrowUp, CalendarDays, Cpu, Database, DollarSign, Gauge, HardDrive, History, MemoryStick, RadioTower, Server, Sparkles, Zap } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import clsx from 'clsx'

const REFRESH_MS = 2500
const HISTORY_SAMPLE_LIMIT = 500
const CHART_WIDTH = 1200
const CHART_HEIGHT = 170
const CHART_LEFT = 24
const CHART_RIGHT = 24
const CHART_TOP = 30
const CHART_BASELINE = 146
const FAILURE_RATE_EXCLUDED_STATUS_CODES = new Set([401, 403, 502, 504, 524])

function asArray(value) {
    return Array.isArray(value) ? value : []
}

function formatNumber(value) {
    const n = Number(value) || 0
    if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
    if (n >= 10_000) return `${(n / 10_000).toFixed(1)}w`
    return String(n)
}

function optionalNumber(value) {
    const n = Number(value)
    return Number.isFinite(n) ? n : null
}

function formatElapsed(ms) {
    const n = Number(ms) || 0
    if (n <= 0) return '-'
    if (n < 1000) return `${n}ms`
    return `${(n / 1000).toFixed(n < 10_000 ? 2 : 1)}s`
}

function formatRate(value) {
    const n = Number(value) || 0
    if (n >= 100) return n.toFixed(0)
    if (n >= 10) return n.toFixed(1)
    return n.toFixed(2)
}

function formatTokenRate(value) {
    return `${formatRate(value)}/s`
}

function formatCurrency(value) {
    const n = Number(value) || 0
    if (n >= 1000) return `$${n.toLocaleString(undefined, { maximumFractionDigits: 0 })}`
    if (n >= 1) return `$${n.toFixed(2)}`
    return `$${n.toFixed(4)}`
}

function formatPercent(value) {
    const n = Number(value) || 0
    return `${n.toFixed(n >= 10 ? 1 : 2)}%`
}

function formatGaugePercent(value) {
    const n = Number(value) || 0
    if (n <= 0) return '0%'
    if (n < 1) return `${n.toFixed(2)}%`
    if (n < 10) return `${n.toFixed(1)}%`
    return `${Math.round(n)}%`
}

function formatBytes(value) {
    let n = Number(value) || 0
    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
    let index = 0
    while (n >= 1024 && index < units.length - 1) {
        n /= 1024
        index += 1
    }
    const digits = index === 0 ? 0 : n >= 100 ? 0 : n >= 10 ? 1 : 2
    return `${n.toFixed(digits)} ${units[index]}`
}

function formatBandwidth(value) {
    return `${formatBytes(value)}/s`
}

function tokenWindowTotals(metrics, key) {
    return metrics?.token_windows?.[key]?.totals || {}
}

function loadStatusLabel(status) {
    if (status === 'critical') return '高负载'
    if (status === 'warn') return '偏高'
    if (status === 'ok') return '平稳'
    return '未知'
}

function accountName(account, index) {
    return account.name || account.remark || account.identifier || account.email || account.mobile || `account-${index + 1}`
}

function accountID(account, index) {
    return account.identifier || account.email || account.mobile || `账号 ${index + 1}`
}

function isNeutralStop(item) {
    if (!item || (item.status !== 'stopped' && item.status !== 'error')) return false
    const reason = String(item.finish_reason || '').toLowerCase()
    return reason === 'context_cancelled' || reason === 'server_restart'
}

function statusCodeOf(item) {
    const code = Number(item?.status_code)
    return Number.isFinite(code) ? code : 0
}

function isFailureRateExcludedItem(item) {
    if (!item || (item.status !== 'stopped' && item.status !== 'error')) return false
    return FAILURE_RATE_EXCLUDED_STATUS_CODES.has(statusCodeOf(item))
}

function isFailedHistoryItem(item) {
    if (!item) return false
    if (isNeutralStop(item)) return false
    if (isFailureRateExcludedItem(item)) return false
    if (item.status === 'error') return true
    return item.status === 'stopped'
}

function chartTone(item) {
    if (isFailedHistoryItem(item)) return 'danger'
    if (isNeutralStop(item) || isFailureRateExcludedItem(item) || item?.status === 'streaming' || item?.status === 'queued') return 'warn'
    return 'ok'
}

function clamp(value, min, max) {
    return Math.min(max, Math.max(min, value))
}

function buildChartBars(items) {
    const source = items.slice(0, 28).reverse()
    const samples = source.length > 0
        ? source.map((item, index) => {
            const elapsed = Number(item.elapsed_ms) || 0
            return {
                label: item.status || `${index + 1}`,
                value: Math.max(1, elapsed),
                displayValue: formatElapsed(elapsed),
                status: item.status || 'unknown',
                model: item.model || 'model',
                tone: chartTone(item),
            }
        })
        : [
            { label: 'T-7', value: 18, displayValue: '18ms', status: 'sample', model: 'sample', tone: 'ok' },
            { label: 'T-6', value: 27, displayValue: '27ms', status: 'sample', model: 'sample', tone: 'ok' },
            { label: 'T-5', value: 22, displayValue: '22ms', status: 'sample', model: 'sample', tone: 'ok' },
            { label: 'T-4', value: 36, displayValue: '36ms', status: 'sample', model: 'sample', tone: 'ok' },
            { label: 'T-3', value: 31, displayValue: '31ms', status: 'sample', model: 'sample', tone: 'ok' },
            { label: 'T-2', value: 44, displayValue: '44ms', status: 'sample', model: 'sample', tone: 'ok' },
            { label: 'T-1', value: 39, displayValue: '39ms', status: 'sample', model: 'sample', tone: 'ok' },
            { label: 'Now', value: 52, displayValue: '52ms', status: 'sample', model: 'sample', tone: 'ok' },
        ]
    const max = Math.max(...samples.map(item => item.value), 1)
    const plotWidth = CHART_WIDTH - CHART_LEFT - CHART_RIGHT
    const slotWidth = plotWidth / Math.max(samples.length, 1)
    const barWidth = Math.min(34, Math.max(9, slotWidth * 0.42))
    const bars = samples.map((item, index) => {
        const height = Math.max(4, (item.value / max) * (CHART_BASELINE - CHART_TOP))
        const x = CHART_LEFT + slotWidth * index + (slotWidth - barWidth) / 2
        const y = CHART_BASELINE - height
        const centerX = x + barWidth / 2
        const tipX = clamp(centerX / CHART_WIDTH * 100, 5, 95)
        const rawTipY = y / CHART_HEIGHT * 100
        const tipPlacement = rawTipY < 28 ? 'below' : 'above'
        const tipY = tipPlacement === 'below'
            ? clamp((y + 8) / CHART_HEIGHT * 100, 18, 82)
            : clamp(rawTipY, 28, 82)
        return {
            ...item,
            x,
            y,
            width: barWidth,
            height,
            tipX,
            tipY,
            tipPlacement,
            title: `${item.model} · ${item.status} · ${item.displayValue}`,
        }
    })
    return { bars, max }
}

function loadCapacityFromQueue(queue, totalAccounts, inUse) {
    const globalLimit = Number(queue.global_max_inflight) || 0
    const recommended = Number(queue.recommended_concurrency) || 0
    const maxPerAccount = Number(queue.max_inflight_per_account) || 0
    return globalLimit || recommended || Math.max(totalAccounts * maxPerAccount, totalAccounts, inUse, 1)
}

function loadPercentFromQueue(inUse, capacity) {
    if (capacity <= 0 || inUse <= 0) return 0
    const raw = Math.min(100, inUse / capacity * 100)
    if (raw < 1) return Number(raw.toFixed(2))
    if (raw < 10) return Number(raw.toFixed(1))
    return Math.round(raw)
}

function MetricCard({ icon: Icon, label, value, hint, tone = 'blue' }) {
    return (
        <div className={clsx('metric-tile group', tone === 'emerald' && 'metric-tile-emerald', tone === 'amber' && 'metric-tile-amber', tone === 'cyan' && 'metric-tile-cyan')}>
            <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                    <p className="ops-kicker">{label}</p>
                    <div className="mt-2 text-3xl font-black tracking-tight tabular-nums text-foreground">{value}</div>
                </div>
                <div className="w-10 h-10 rounded-lg border border-blue-100 bg-blue-50 flex items-center justify-center text-primary transition-transform duration-200 group-hover:scale-105">
                    <Icon className="w-4 h-4" />
                </div>
            </div>
            <div className="mt-4 text-[11px] font-bold text-muted-foreground truncate">{hint}</div>
        </div>
    )
}

function MiniBar({ label, value, max, tone = 'blue' }) {
    const percent = max > 0 ? Math.min(100, Math.round((Number(value) || 0) / max * 100)) : 0
    return (
        <div className="space-y-2">
            <div className="flex items-center justify-between gap-3 text-xs">
                <span className="font-bold text-slate-600">{label}</span>
                <span className="font-black tabular-nums text-slate-950">{value}</span>
            </div>
            <div className="h-2 rounded-full bg-slate-100 overflow-hidden">
                <div
                    className={clsx('h-full rounded-full transition-all duration-500', tone === 'emerald' ? 'bg-emerald-500' : tone === 'amber' ? 'bg-amber-500' : 'bg-blue-500')}
                    style={{ width: `${percent}%` }}
                />
            </div>
        </div>
    )
}

export default function OverviewContainer({ config, authFetch, onMessage }) {
    const apiFetch = authFetch || fetch
    const accounts = asArray(config?.accounts)
    const proxies = asArray(config?.proxies)
    const [queueStatus, setQueueStatus] = useState(null)
    const [historyItems, setHistoryItems] = useState([])
    const [historyMeta, setHistoryMeta] = useState({ total: 0, limit: 0, count: 0 })
    const [overviewMetrics, setOverviewMetrics] = useState(null)
    const [hoveredBar, setHoveredBar] = useState(null)

    useEffect(() => {
        let disposed = false
        async function loadOverviewData() {
            try {
                const [queueRes, historyRes, metricsRes] = await Promise.all([
                    apiFetch('/admin/queue/status'),
                    apiFetch(`/admin/chat-history?offset=0&limit=${HISTORY_SAMPLE_LIMIT}`),
                    apiFetch('/admin/metrics/overview'),
                ])
                if (disposed) return
                if (queueRes.ok) {
                    setQueueStatus(await queueRes.json())
                }
                if (historyRes.ok) {
                    const data = await historyRes.json()
                    const items = asArray(data.items)
                    setHistoryItems(items)
                    setHistoryMeta({
                        total: Number(data.total) || 0,
                        limit: Number(data.limit) || 0,
                        count: Number(data.count) || items.length,
                    })
                }
                if (metricsRes.ok) {
                    setOverviewMetrics(await metricsRes.json())
                }
            } catch (err) {
                if (!disposed && onMessage) {
                    onMessage(`总览数据刷新失败: ${err.message}`, 'error')
                }
            }
        }
        loadOverviewData()
        const timer = setInterval(loadOverviewData, REFRESH_MS)
        return () => {
            disposed = true
            clearInterval(timer)
        }
    }, [apiFetch, onMessage])

    const stats = useMemo(() => {
        const success = historyItems.filter(item => item.status === 'success').length
        const failed = historyItems.filter(isFailedHistoryItem).length
        const excluded = historyItems.filter(isFailureRateExcludedItem).length
        const streaming = historyItems.filter(item => item.status === 'streaming').length
        const queued = historyItems.filter(item => item.status === 'queued').length
        const finished = success + failed
        const successRate = finished > 0 ? Math.round(success / finished * 100) : 100
        const totalElapsed = historyItems.reduce((sum, item) => sum + (Number(item.elapsed_ms) || 0), 0)
        const avgElapsed = historyItems.length > 0 ? Math.round(totalElapsed / historyItems.length) : 0
        return { success, failed, excluded, streaming, queued, successRate, avgElapsed }
    }, [historyItems])

    const queue = queueStatus || {}
    const totalAccounts = Number(queue.total) || accounts.length
    const inUse = Number(queue.in_use) || 0
    const available = Number(queue.available) || Math.max(totalAccounts - inUse, 0)
    const waiting = Number(queue.waiting) || 0
    const loadCapacity = loadCapacityFromQueue(queue, totalAccounts, inUse)
    const loadPercent = loadPercentFromQueue(inUse, loadCapacity)
    const gaugeVisualPercent = loadPercent > 0 && loadPercent < 1 ? 0.75 : loadPercent
    const chart = buildChartBars(historyItems)
    const recentAccounts = accounts.slice(0, 6)
    const recentLogs = historyItems.slice(0, 7)
    const queueCapacity = Number(queue.max_queue_size) || Math.max(waiting, 1)
    const metrics = overviewMetrics || {}
    const throughput = metrics.throughput || {}
    const cache = metrics.cache || {}
    const cacheHits = Number(cache.hits) || 0
    const cacheMisses = Number(cache.misses) || 0
    const cacheStores = Number(cache.stores) || 0
    const cacheableLookups = Number(cache.cacheable_lookups) || cacheHits + cacheStores
    const cacheableMisses = Number(cache.cacheable_misses) || cacheStores
    const uncacheableMisses = Number(cache.uncacheable_misses) || 0
    const totalCacheHitRate = Number(cache.hit_rate) || 0
    const cacheableHitRate = Number(cache.cacheable_hit_rate) || 0
    const cacheableMissRate = Number(cache.cacheable_miss_rate) || (cacheableLookups > 0 ? (cacheableMisses * 100) / cacheableLookups : 0)
    const uncacheableMissRate = cacheMisses > 0 ? (uncacheableMisses * 100) / cacheMisses : 0
    const currentInputPrefix = metrics.current_input_prefix || {}
    const prefixApplied = Number(currentInputPrefix.applied) || 0
    const prefixReused = Number(currentInputPrefix.reused) || 0
    const prefixRefreshes = Number(currentInputPrefix.refreshes) || 0
    const prefixFallbackFullUploads = Number(currentInputPrefix.fallback_full_uploads) || 0
    const prefixReuseRate = Number(currentInputPrefix.reuse_rate) || 0
    const prefixActiveStates = Number(currentInputPrefix.active_states) || 0
    const prefixTailAvg = Number(currentInputPrefix.tail_chars_avg) || 0
    const prefixTailP95 = Number(currentInputPrefix.tail_chars_p95) || 0
    const prefixFileMsAvg = Number(currentInputPrefix.current_input_file_ms_avg) || 0
    const prefixFileMsReusedAvg = Number(currentInputPrefix.current_input_file_ms_reused_avg) || 0
    const prefixFileMsRefreshAvg = Number(currentInputPrefix.current_input_file_ms_refresh_avg) || 0
    const historyMetrics = metrics.history || {}
    const metricsHistoryTotal = optionalNumber(historyMetrics.total)
    const metaHistoryTotal = optionalNumber(historyMeta.total)
    const totalRequests = Math.max(0, metricsHistoryTotal ?? metaHistoryTotal ?? historyItems.length)
    const successRateForDisplay = optionalNumber(historyMetrics.success_rate) ?? stats.successRate
    const failureRateEligibleTotal = optionalNumber(historyMetrics.eligible_total) ?? (stats.success + stats.failed)
    const excludedFromFailureRate = optionalNumber(historyMetrics.excluded_from_failure_rate) ?? stats.excluded
    const tokenStats = metrics.tokens || {}
    const windowTokens = tokenStats.window || {}
    const totalTokens = tokenStats.total || {}
    const tokenUsageWindows = [
        { key: '24h', label: '24 小时', tone: 'blue' },
        { key: '7d', label: '7 天', tone: 'emerald' },
        { key: '15d', label: '15 天', tone: 'cyan' },
        { key: '30d', label: '30 天', tone: 'amber' },
    ].map(item => {
        const totals = tokenWindowTotals(metrics, item.key)
        return {
            ...item,
            total: Number(totals.total_tokens) || 0,
            input: Number(totals.input_tokens) || 0,
            output: Number(totals.output_tokens) || 0,
            requests: Number(totals.requests) || 0,
        }
    })
    const cost = metrics.cost || {}
    const host = metrics.host || {}
    const hostCpu = host.cpu || {}
    const hostMemory = host.memory || {}
    const hostDisk = host.disk || {}
    const hostLoad = host.load || {}
    const hostBandwidth = host.bandwidth || {}
    const windowSeconds = Number(metrics.window_seconds) || 60

    return (
        <div className="overview-grid space-y-4">
            <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-3">
                <MetricCard icon={Activity} label="实时 QPS" value={formatRate(throughput.qps)} hint={`近 ${windowSeconds} 秒 ${formatNumber(throughput.requests_in_window)} 次请求`} />
                <MetricCard icon={Zap} label="实时 Token" value={formatTokenRate(throughput.tokens_per_second)} hint={`近 ${windowSeconds} 秒 ${formatNumber(windowTokens.total_tokens)} Tokens`} tone="emerald" />
                <MetricCard icon={Database} label="总 Token" value={formatNumber(totalTokens.total_tokens)} hint={`${formatNumber(totalTokens.input_tokens)} 输入 / ${formatNumber(totalTokens.output_tokens)} 输出`} tone="cyan" />
                <MetricCard icon={DollarSign} label="总费用" value={formatCurrency(cost.total_usd)} hint={`${cost.currency || 'USD'} · 按官方价格估算`} tone="amber" />
            </div>

            <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-6 gap-3">
                <MetricCard icon={Cpu} label="服务器 CPU" value={formatPercent(hostCpu.percent)} hint={`${hostCpu.cores || 0} Core · 实时采样`} />
                <MetricCard icon={MemoryStick} label="内存" value={formatPercent(hostMemory.percent)} hint={`${formatBytes(hostMemory.used_bytes)} / ${formatBytes(hostMemory.total_bytes)}`} tone="emerald" />
                <MetricCard icon={HardDrive} label="磁盘" value={formatPercent(hostDisk.percent)} hint={`${formatBytes(hostDisk.used_bytes)} / ${formatBytes(hostDisk.total_bytes)}`} tone="amber" />
                <MetricCard icon={Gauge} label="负载状态" value={formatRate(hostLoad.load1)} hint={`5m ${formatRate(hostLoad.load5)} / 15m ${formatRate(hostLoad.load15)} · ${loadStatusLabel(hostLoad.status)}`} />
                <MetricCard icon={ArrowUp} label="带宽上行" value={formatBandwidth(hostBandwidth.tx_bytes_per_sec)} hint={`累计 ${formatBytes(hostBandwidth.tx_total_bytes)}`} tone="cyan" />
                <MetricCard icon={ArrowDown} label="带宽下行" value={formatBandwidth(hostBandwidth.rx_bytes_per_sec)} hint={`累计 ${formatBytes(hostBandwidth.rx_total_bytes)}`} tone="emerald" />
            </div>

            <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-3">
                <MetricCard icon={Sparkles} label="Prefix 复用率" value={formatPercent(prefixReuseRate)} hint={`${formatNumber(prefixReused)} / ${formatNumber(prefixApplied)} applied · ${formatNumber(prefixActiveStates)} active`} tone="emerald" />
                <MetricCard icon={Database} label="Checkpoint 刷新" value={formatNumber(prefixRefreshes)} hint={`${formatNumber(prefixFallbackFullUploads)} 次 full upload fallback`} tone="amber" />
                <MetricCard icon={History} label="Tail 大小" value={formatBytes(prefixTailAvg)} hint={`p95 ${formatBytes(prefixTailP95)} · inline rolling tail`} tone="cyan" />
                <MetricCard icon={Zap} label="Current Input 耗时" value={formatElapsed(prefixFileMsAvg)} hint={`reuse ${formatElapsed(prefixFileMsReusedAvg)} / refresh ${formatElapsed(prefixFileMsRefreshAvg)}`} />
            </div>

            <div className="ops-panel p-4">
                <div className="flex items-center justify-between gap-4">
                    <div>
                        <p className="ops-kicker">Token Usage</p>
                        <h2 className="ops-heading mt-1">Token 使用量统计</h2>
                    </div>
                    <CalendarDays className="w-5 h-5 text-primary" />
                </div>
                <div className="mt-4 grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-3">
                    {tokenUsageWindows.map(item => (
                        <div key={item.key} className={clsx('token-window-card', `token-window-card-${item.tone}`)}>
                            <div className="flex items-start justify-between gap-3">
                                <div>
                                    <span>{item.label}</span>
                                    <strong>{formatNumber(item.total)}</strong>
                                </div>
                                <em>{formatNumber(item.requests)} 次</em>
                            </div>
                            <div className="mt-3 grid grid-cols-2 gap-2 text-[11px] font-black text-muted-foreground">
                                <div>输入 {formatNumber(item.input)}</div>
                                <div>输出 {formatNumber(item.output)}</div>
                            </div>
                        </div>
                    ))}
                </div>
            </div>

            <div className="grid grid-cols-1 xl:grid-cols-[minmax(0,1.45fr),minmax(320px,0.85fr)] gap-4">
                <div className="ops-panel p-4 min-h-[310px]">
                    <div className="flex items-start justify-between gap-4">
                        <div>
                            <p className="ops-kicker">Realtime Throughput</p>
                            <h2 className="ops-heading mt-1">请求趋势图</h2>
                        </div>
                        <div className="inline-flex items-center gap-2 rounded-md border border-blue-100 bg-blue-50 px-2.5 py-1.5 text-xs font-bold text-blue-700">
                            <RadioTower className="w-3.5 h-3.5" />
                            SSE Live
                        </div>
                    </div>
                    <div className="overview-chart mt-5" onMouseLeave={() => setHoveredBar(null)}>
                        <svg viewBox={`0 0 ${CHART_WIDTH} ${CHART_HEIGHT}`} preserveAspectRatio="none" role="img" aria-label="请求趋势图">
                            <defs>
                                <linearGradient id="overviewBarGradient" x1="0" x2="0" y1="0" y2="1">
                                    <stop offset="0%" stopColor="#006eff" />
                                    <stop offset="100%" stopColor="#10b981" />
                                </linearGradient>
                            </defs>
                            {[38, 66, 94, 122, 150].map(y => (
                                <line key={y} x1={CHART_LEFT} x2={CHART_WIDTH - CHART_RIGHT} y1={y} y2={y} className="overview-chart-grid" />
                            ))}
                            <line x1={CHART_LEFT} x2={CHART_WIDTH - CHART_RIGHT} y1={CHART_BASELINE} y2={CHART_BASELINE} className="overview-chart-axis" />
                            {chart.bars.map((bar, index) => (
                                <g
                                    key={`${bar.title}-${index}`}
                                    className="overview-chart-bar-group"
                                    tabIndex="0"
                                    aria-label={bar.title}
                                    onMouseEnter={() => setHoveredBar(bar)}
                                    onFocus={() => setHoveredBar(bar)}
                                    onBlur={() => setHoveredBar(null)}
                                >
                                    <title>{bar.title}</title>
                                    <rect
                                        x={bar.x.toFixed(1)}
                                        y={bar.y.toFixed(1)}
                                        width={bar.width.toFixed(1)}
                                        height={bar.height.toFixed(1)}
                                        rx="3"
                                        className={clsx('overview-chart-bar', `overview-chart-bar-${bar.tone}`)}
                                    />
                                </g>
                            ))}
                        </svg>
                        {hoveredBar && (
                            <div
                                className={clsx('overview-chart-tooltip', hoveredBar.tipPlacement === 'below' && 'overview-chart-tooltip-below')}
                                style={{ left: `${hoveredBar.tipX}%`, top: `${hoveredBar.tipY}%` }}
                            >
                                <strong>{hoveredBar.displayValue}</strong>
                                <span>{hoveredBar.status}</span>
                            </div>
                        )}
                    </div>
                    <div className="mt-3 grid grid-cols-2 md:grid-cols-3 xl:grid-cols-6 gap-3">
                        <div className="overview-signal">
                            <span>总请求</span>
                            <strong>{formatNumber(totalRequests)}</strong>
                        </div>
                        <div className="overview-signal">
                            <span>成功率</span>
                            <strong>{formatPercent(successRateForDisplay)}</strong>
                            <em>{formatNumber(failureRateEligibleTotal)} 计入 / {formatNumber(excludedFromFailureRate)} 排除</em>
                        </div>
                        <div className="overview-signal">
                            <span>平均耗时</span>
                            <strong>{formatElapsed(stats.avgElapsed)}</strong>
                        </div>
                        <div className="overview-signal">
                            <span>缓存命中率</span>
                            <strong>{formatPercent(cacheableHitRate)}</strong>
                            <em>{formatNumber(cacheHits)} 命中 / {formatNumber(cacheableLookups)} 可缓存</em>
                        </div>
                        <div className="overview-signal">
                            <span>缓存未命中率</span>
                            <strong>{formatPercent(cacheableMissRate)}</strong>
                            <em>{formatNumber(cacheableMisses)} 写入 / 总查询命中 {formatPercent(totalCacheHitRate)}</em>
                        </div>
                        <div className="overview-signal">
                            <span>不可缓存未命中</span>
                            <strong>{formatPercent(uncacheableMissRate)}</strong>
                            <em>{formatNumber(uncacheableMisses)} 不可存 / {formatNumber(cacheMisses)} 未命中</em>
                        </div>
                    </div>
                </div>

                <div className="ops-panel p-4 min-h-[310px]">
                    <div className="flex items-start justify-between gap-4">
                        <div>
                            <p className="ops-kicker">Pool Load</p>
                            <h2 className="ops-heading mt-1">账号负载仪表</h2>
                        </div>
                        <Gauge className="w-5 h-5 text-primary" />
                    </div>
                    <div className="overview-gauge-wrap">
                        <div className="overview-gauge" style={{ '--gauge': `${gaugeVisualPercent}%` }}>
                            <div className="overview-gauge-inner">
                                <span>{formatGaugePercent(loadPercent)}</span>
                                <small>{inUse}/{formatNumber(loadCapacity)}</small>
                            </div>
                        </div>
                    </div>
                    <div className="space-y-3">
                        <MiniBar label="可用账号" value={available} max={Math.max(totalAccounts, available, 1)} tone="emerald" />
                        <MiniBar label="占用线程" value={inUse} max={loadCapacity} />
                        <MiniBar label="等待队列" value={waiting} max={queueCapacity} tone="amber" />
                    </div>
                </div>
            </div>

            <div className="grid grid-cols-1 2xl:grid-cols-[minmax(0,1fr),minmax(360px,0.78fr)] gap-4">
                <div className="ops-panel p-4">
                    <div className="flex items-center justify-between gap-4">
                        <div>
                            <p className="ops-kicker">Account Pool</p>
                            <h2 className="ops-heading mt-1">账号池运行快照</h2>
                        </div>
                        <Server className="w-5 h-5 text-primary" />
                    </div>
                    <div className="mt-4 overflow-hidden rounded-lg border border-slate-200">
                        <div className="grid grid-cols-[minmax(180px,1fr),110px,110px,110px] gap-0 bg-slate-50 px-4 py-2 text-[11px] font-black uppercase text-slate-500">
                            <span>账号</span>
                            <span>状态</span>
                            <span>会话</span>
                            <span>代理</span>
                        </div>
                        <div className="divide-y divide-slate-200 bg-white/80">
                            {recentAccounts.length === 0 && (
                                <div className="px-4 py-8 text-center text-sm text-muted-foreground">暂无账号数据</div>
                            )}
                            {recentAccounts.map((account, index) => (
                                <div key={`${accountID(account, index)}-${index}`} className="grid grid-cols-[minmax(180px,1fr),110px,110px,110px] items-center gap-0 px-4 py-3 text-sm table-row-hover">
                                    <div className="min-w-0">
                                        <div className="font-black text-slate-950 truncate">{accountName(account, index)}</div>
                                        <div className="mt-0.5 text-xs text-muted-foreground truncate">{accountID(account, index)}</div>
                                    </div>
                                    <div className="inline-flex items-center gap-2 text-xs font-bold text-emerald-700">
                                        <span className="status-dot status-dot-ok" />
                                        在线
                                    </div>
                                    <div className="text-xs font-bold text-slate-600">{account.sessions_count ?? account.session_count ?? '-'}</div>
                                    <div className="text-xs font-bold text-slate-600 truncate">{account.proxy_name || account.proxy || '直连'}</div>
                                </div>
                            ))}
                        </div>
                    </div>
                </div>

                <div className="overview-status ops-panel p-4 overflow-hidden">
                    <div className="flex items-start gap-4">
                        <div className="min-w-0">
                            <p className="ops-kicker">DeepSeek_Web_To_API Operations</p>
                            <h2 className="mt-1 text-xl font-black text-slate-950">运维状态助手</h2>
                            <p className="mt-2 text-sm leading-6 text-muted-foreground">已接入账号池、SSE 流、历史记录与代理状态。</p>
                        </div>
                    </div>
                    <div className="mt-5 grid grid-cols-2 gap-3">
                        <div className="overview-status-chip"><Cpu className="w-4 h-4" />并发 {inUse}</div>
                        <div className="overview-status-chip"><Database className="w-4 h-4" />代理 {proxies.length}</div>
                        <div className="overview-status-chip"><Zap className="w-4 h-4" />等待 {waiting}</div>
                        <div className="overview-status-chip"><Sparkles className="w-4 h-4" />实时刷新</div>
                    </div>
                </div>
            </div>

            <div className="ops-panel p-4">
                <div className="flex items-center justify-between gap-4">
                    <div>
                        <p className="ops-kicker">Live Event Stream</p>
                        <h2 className="ops-heading mt-1">实时对话流日志</h2>
                    </div>
                    <History className="w-5 h-5 text-primary" />
                </div>
                <div className="mt-4 grid grid-cols-1 lg:grid-cols-7 gap-2">
                    {recentLogs.length === 0 && (
                        <div className="lg:col-span-7 rounded-lg border border-dashed border-slate-200 bg-white/70 py-10 text-center text-sm text-muted-foreground">还没有历史流事件</div>
                    )}
                    {recentLogs.map(item => (
                        <div key={item.id} className="log-strip rounded-lg border px-3 py-3 lg:col-span-1 min-h-[112px]">
                            <div className="flex items-center justify-between gap-2">
                                <span className={clsx('status-dot', isFailedHistoryItem(item) ? 'status-dot-danger' : (isNeutralStop(item) || item.status === 'streaming' || item.status === 'queued') ? 'status-dot-warn' : 'status-dot-ok')} />
                                <span className="text-[10px] font-black uppercase text-muted-foreground">{item.status || 'streaming'}</span>
                            </div>
                            <div className="mt-3 text-xs font-black text-slate-950 truncate">{item.model || 'model'}</div>
                            <div className="mt-2 text-[11px] leading-5 text-muted-foreground line-clamp-2">{item.preview || item.user_input || '等待内容写入...'}</div>
                            <div className="mt-3 text-[10px] font-bold text-slate-500">{formatElapsed(item.elapsed_ms)}</div>
                        </div>
                    ))}
                </div>
            </div>
        </div>
    )
}
