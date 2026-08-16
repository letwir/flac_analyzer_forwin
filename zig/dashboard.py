"""
zig/dashboard.py
================
Orchestrator の Prometheus メトリクス (:2112/metrics) をリアルタイムにポーリング・解析し、
1ファイルあたり・1曲（トラック）あたりの所要時間、処理スループット、ETA、
RAM/ディスク空き容量、ワーカー稼働状況をターミナル上に超美麗に TUI 描画するリアルタイム進捗ダッシュボードですわ！
"""

import argparse
import os
import re
import sys
import time
import urllib.request
import urllib.error

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")
if hasattr(sys.stderr, "reconfigure"):
    sys.stderr.reconfigure(encoding="utf-8")

# Rich ライブラリのインポート試行（存在しない場合は ANSI フォールバック）
try:
    from rich.console import Console
    from rich.table import Table
    from rich.panel import Panel
    from rich.layout import Layout
    from rich.live import Live
    from rich.text import Text
    HAVE_RICH = True
except ImportError:
    HAVE_RICH = False

def fetch_prometheus_metrics(url: str, timeout: float = 2.0) -> dict[str, float]:
    """Prometheus /metrics エンドポイントからテキストを取得し、パースして辞書化いたしますわ！"""
    metrics: dict[str, float] = {}
    req = urllib.request.Request(url, headers={"User-Agent": "FlacAnalyzerDashboard/1.0"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            content = resp.read().decode("utf-8", errors="replace")
    except Exception as e:
        return {"_error": 1.0, "_error_msg": str(e)}

    # Prometheus テキストフォーマットの解析
    for line in content.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue

        # 例: analyzer_tasks_total{status="success"} 42
        # 例: analyzer_avg_task_duration_seconds 8.45
        match = re.match(r"^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{([^}]*)\})?\s+([+-]?[0-9]*\.?[0-9]+(?:[eE][+-]?[0-9]+)?)$", line)
        if match:
            metric_name = match.group(1)
            labels = match.group(2) or ""
            val = float(match.group(3))

            if labels:
                # ラベル付きメトリクス (例: analyzer_tasks_total_status_success)
                label_parts = []
                for kv in labels.split(","):
                    if "=" in kv:
                        k, v = kv.split("=", 1)
                        clean_k = k.strip()
                        clean_v = v.strip().strip('"').strip("'")
                        label_parts.append(f"{clean_k}_{clean_v}")
                full_key = f"{metric_name}_{'_'.join(label_parts)}"
                metrics[full_key] = val
            else:
                metrics[metric_name] = val

    return metrics

def format_duration(seconds: float) -> str:
    """秒数を人間可読な時間文字列へフォーマットいたしますわ！"""
    if seconds <= 0:
        return "0.0s"
    if seconds < 60:
        return f"{seconds:.1f}s"
    minutes = int(seconds // 60)
    rem_sec = seconds % 60
    if minutes < 60:
        return f"{minutes}m {rem_sec:.1f}s"
    hours = int(minutes // 60)
    rem_min = minutes % 60
    return f"{hours}h {rem_min}m"

def render_rich_dashboard(console: Console, metrics: dict[str, float]) -> Panel:
    """Rich ライブラリを用いた超美麗な TUI パネルの構築ですわ！"""
    if "_error" in metrics:
        err_msg = metrics.get("_error_msg", "Connection failed")
        return Panel(
            Text(f"❌ Orchestrator メトリクスサーバーに接続できませんわ: {err_msg}\n(Orchestrator が起動しているか、ポート 2112 をご確認くださいませ)", style="bold red"),
            title="[bold red]FLAC Analyzer Dashboard - OFFLINE[/bold red]",
            border_style="red"
        )

    # 1. 所要時間メトリクス
    last_task = metrics.get("analyzer_last_task_duration_seconds", 0.0)
    avg_task = metrics.get("analyzer_avg_task_duration_seconds", 0.0)
    last_file = metrics.get("analyzer_last_file_duration_seconds", 0.0)
    avg_file = metrics.get("analyzer_avg_file_duration_seconds", 0.0)

    # 2. スループット＆ETA
    tasks_pm = metrics.get("analyzer_tasks_per_minute", 0.0)
    files_pm = metrics.get("analyzer_files_per_minute", 0.0)
    eta_sec = metrics.get("analyzer_eta_seconds", 0.0)
    q_len = int(metrics.get("analyzer_queue_length", 0))

    # 3. 処理タスク数・ファイル数
    tasks_succ = int(metrics.get("analyzer_tasks_total_status_success", 0))
    tasks_err = int(metrics.get("analyzer_tasks_total_status_error", 0))
    tasks_oom = int(metrics.get("analyzer_tasks_total_status_oom_failed", 0))
    tasks_skip = int(metrics.get("analyzer_tasks_total_status_skipped", 0))
    
    files_succ = int(metrics.get("analyzer_files_total_status_success", 0))
    files_err = int(metrics.get("analyzer_files_total_status_error", 0))
    files_skip = int(metrics.get("analyzer_files_total_status_skipped", 0))

    # 4. システムリソース
    workers = int(metrics.get("analyzer_active_workers", 0))
    demucs_slots = int(metrics.get("analyzer_demucs_slots_in_use", 0))
    ram_bytes = metrics.get("analyzer_ram_available_bytes", 0.0)
    disk_bytes = metrics.get("analyzer_disk_free_bytes", 0.0)
    errors_total = int(metrics.get("analyzer_errors_total", 0))

    ram_gb = ram_bytes / (1024 ** 3)
    disk_gb = disk_bytes / (1024 ** 3)

    # 5. ステージ別所要時間 (Stage Latency Breakdown)
    stages = [
        ("hash_check", "Fast MD5/重複確認"),
        ("shm_alloc", "SHM確保/Lock"),
        ("demucs", "Demucs分離 (GPU/ONNX)"),
        ("librosa", "Librosa特徴量 (CPU)"),
        ("tensor", "Tensor特徴量 (GPU)"),
        ("essentia", "Essentia特徴量 (C++)"),
        ("flac_tagger", "FLACタグ書き込み (Disk)"),
        ("db_ingest", "DBインジェスト (Postgres)"),
    ]
    stage_table = Table(title="🔍 [bold cyan]ボトルネック・ステージ別所要時間 (Stage Breakdown)[/bold cyan]", expand=True)
    stage_table.add_column("ステージ (Stage)", style="bold white")
    stage_table.add_column("平均 (Avg)", style="green")
    stage_table.add_column("直近 (Last)", style="yellow")

    has_stage_data = False
    for stg_key, stg_name in stages:
        avg_v = metrics.get(f"analyzer_avg_stage_duration_seconds_stage_{stg_key}", 0.0)
        last_v = metrics.get(f"analyzer_last_stage_duration_seconds_stage_{stg_key}", 0.0)
        if avg_v > 0 or last_v > 0:
            has_stage_data = True
            stage_table.add_row(stg_name, format_duration(avg_v), format_duration(last_v))

    if not has_stage_data:
        stage_table.add_row("[dim]待機中 / 計測データ収集中[/dim]", "-", "-")

    # 6. リソース待機・競合時間 (Contention & Wait)
    demucs_wait = metrics.get("analyzer_last_demucs_wait_seconds", 0.0)
    tensor_wait = metrics.get("analyzer_last_tensor_wait_seconds", 0.0)
    gk_wait = metrics.get("analyzer_last_gatekeeper_wait_seconds", 0.0)
    demucs_waiters = int(metrics.get("analyzer_demucs_queue_waiters", 0))
    tensor_waiters = int(metrics.get("analyzer_tensor_queue_waiters", 0))

    wait_table = Table(title="⏳ [bold cyan]リソース競合 ＆ 待機時間 (Contention & Wait)[/bold cyan]", expand=True)
    wait_table.add_column("待機項目 (Resource)", style="bold white")
    wait_table.add_column("直近待機時間 (Last Wait)", style="magenta")
    wait_table.add_column("待機ワーカー数 (Queued)", style="cyan")

    wait_table.add_row("Demucs 実行枠待ち", format_duration(demucs_wait), f"{demucs_waiters} workers")
    wait_table.add_row("Tensor 排他枠待ち", format_duration(tensor_wait), f"{tensor_waiters} workers")
    wait_table.add_row("Gatekeeper 防御待機", format_duration(gk_wait), "RAM/Disk 判定")

    # メインレイアウトテーブル
    main_table = Table.grid(expand=True, padding=(0, 2))
    main_table.add_column(ratio=1)
    main_table.add_column(ratio=1)

    # 左パネル: 所要時間 & スループット
    time_table = Table(title="⏱️ [bold cyan]所要時間 ＆ 処理速度 (Durations & Throughput)[/bold cyan]", expand=True)
    time_table.add_column("項目 (Metric)", style="bold white")
    time_table.add_column("平均値 (Avg)", style="green")
    time_table.add_column("直近値 (Last)", style="yellow")

    time_table.add_row("1ファイルあたり所要時間", format_duration(avg_file), format_duration(last_file))
    time_table.add_row("1曲(トラック)あたり所要時間", format_duration(avg_task), format_duration(last_task))
    time_table.add_row("処理スループット", f"{files_pm:.1f} files/min", f"{tasks_pm:.1f} tracks/min")
    time_table.add_row("残り推定時間 (ETA)", f"[bold magenta]{format_duration(eta_sec)}[/bold magenta]", f"キュー残: [cyan]{q_len}[/cyan] 件")

    # 右パネル: システム状態 ＆ 処理カウンター
    sys_table = Table(title="⚙️ [bold cyan]システムリソース ＆ 稼働統計 (System & Stats)[/bold cyan]", expand=True)
    sys_table.add_column("項目 (Metric)", style="bold white")
    sys_table.add_column("ステータス (Status)", style="cyan")

    sys_table.add_row("稼働ワーカー / Demucs枠", f"[bold green]{workers}[/bold green] workers / [bold yellow]{demucs_slots}[/bold yellow] demucs slots")
    sys_table.add_row("物理RAM 有効空き容量", f"[bold green]{ram_gb:.2f} GB[/bold green]")
    sys_table.add_row("作業ディスク 空き容量", f"[bold green]{disk_gb:.2f} GB[/bold green]")
    sys_table.add_row("完了ファイル総数", f"✅ [green]{files_succ}[/green] 成功 / ❌ [red]{files_err}[/red] 失敗 / ⏭️ [dim]{files_skip}[/dim] スキップ")
    sys_table.add_row("完了トラック総数", f"✅ [green]{tasks_succ}[/green] 成功 / ❌ [red]{tasks_err + tasks_oom}[/red] 失敗 / ⏭️ [dim]{tasks_skip}[/dim] スキップ")
    if errors_total > 0:
        sys_table.add_row("総エラー発生回数", f"[bold red]{errors_total} 回[/bold red]")

    main_table.add_row(time_table, sys_table)
    main_table.add_row(stage_table, wait_table)

    header_text = Text("🎵 FLAC Analyzer リアルタイム進捗ダッシュボード (Win32 Orchestrator Live)", style="bold bright_white on blue", justify="center")
    
    return Panel(
        main_table,
        title=header_text,
        subtitle=f"[dim]Prometheus: http://localhost:2112/metrics | pprof: /debug/pprof/ | Polling: {time.strftime('%H:%M:%S')}[/dim]",
        border_style="bright_blue"
    )

def render_ansi_dashboard(metrics: dict[str, float]):
    """Rich が利用できない場合の高品位 ANSI フォールバックレンダラーですわ！"""
    os.system("cls" if os.name == "nt" else "clear")
    print("==========================================================================")
    print(" 🎵 FLAC Analyzer リアルタイム進捗ダッシュボード (ANSI Mode)")
    print("==========================================================================")
    
    if "_error" in metrics:
        print(f" ❌ Orchestrator メトリクスサーバーに接続できません: {metrics.get('_error_msg')}")
        print(" (Orchestrator が起動しているかご確認ください)")
        print("==========================================================================")
        return

    avg_file = metrics.get("analyzer_avg_file_duration_seconds", 0.0)
    last_file = metrics.get("analyzer_last_file_duration_seconds", 0.0)
    avg_task = metrics.get("analyzer_avg_task_duration_seconds", 0.0)
    last_task = metrics.get("analyzer_last_task_duration_seconds", 0.0)
    tasks_pm = metrics.get("analyzer_tasks_per_minute", 0.0)
    files_pm = metrics.get("analyzer_files_per_minute", 0.0)
    eta_sec = metrics.get("analyzer_eta_seconds", 0.0)
    q_len = int(metrics.get("analyzer_queue_length", 0))

    workers = int(metrics.get("analyzer_active_workers", 0))
    demucs_slots = int(metrics.get("analyzer_demucs_slots_in_use", 0))
    ram_gb = metrics.get("analyzer_ram_available_bytes", 0.0) / (1024 ** 3)
    disk_gb = metrics.get("analyzer_disk_free_bytes", 0.0) / (1024 ** 3)

    tasks_succ = int(metrics.get("analyzer_tasks_total_status_success", 0))
    files_succ = int(metrics.get("analyzer_files_total_status_success", 0))

    print(f" [所要時間] 1ファイル平均: {format_duration(avg_file):<10} (直近: {format_duration(last_file)})")
    print(f" [所要時間] 1曲(トラック)平均: {format_duration(avg_task):<10} (直近: {format_duration(last_task)})")
    print(f" [速度] {files_pm:.1f} files/min | {tasks_pm:.1f} tracks/min | ETA: {format_duration(eta_sec)} (残: {q_len} 件)")
    print(f" [リソース] Active Workers: {workers} | Demucs: {demucs_slots} | RAM: {ram_gb:.2f} GB | Disk: {disk_gb:.2f} GB")
    print(f" [完了実績] Files: {files_succ} 件完了 | Tracks: {tasks_succ} 曲完了")
    print("==========================================================================")

def main():
    parser = argparse.ArgumentParser(description="FLAC Analyzer Realtime CLI Dashboard (Prometheus)")
    parser.add_argument("--url", type=str, default="http://localhost:2112/metrics", help="Prometheus /metrics URL")
    parser.add_argument("--interval", type=float, default=1.0, help="Refresh interval in seconds")
    parser.add_argument("--once", action="store_true", help="Print dashboard once and exit")
    args = parser.parse_args()

    if args.once:
        data = fetch_prometheus_metrics(args.url)
        if HAVE_RICH:
            console = Console()
            console.print(render_rich_dashboard(console, data))
        else:
            render_ansi_dashboard(data)
        return

    if HAVE_RICH:
        console = Console()
        with Live(render_rich_dashboard(console, fetch_prometheus_metrics(args.url)), console=console, refresh_per_second=int(1.0/max(args.interval, 0.2))) as live:
            try:
                while True:
                    time.sleep(args.interval)
                    data = fetch_prometheus_metrics(args.url)
                    live.update(render_rich_dashboard(console, data))
            except KeyboardInterrupt:
                pass
    else:
        try:
            while True:
                data = fetch_prometheus_metrics(args.url)
                render_ansi_dashboard(data)
                time.sleep(args.interval)
        except KeyboardInterrupt:
            pass

if __name__ == "__main__":
    main()
