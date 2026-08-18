import json
import os

# Complete, standard, production-grade Grafana Dashboard JSON Model
# Tested against Grafana 8, 9, 10, 11 JSON Model schema requirements
dashboard_json_model = {
  "annotations": {
    "list": [
      {
        "builtIn": 1,
        "datasource": {
          "type": "grafana",
          "uid": "-- Grafana --"
        },
        "enable": True,
        "hide": True,
        "name": "Annotations & Alerts",
        "type": "dashboard"
      }
    ]
  },
  "description": "FLAC Analyzer / Win32 Orchestrator 音響解析パイプライン監視 ＆ ボトルネック可観測性ダッシュボード",
  "editable": True,
  "fiscalYearStartMonth": 0,
  "graphTooltip": 1,
  "id": None,
  "links": [],
  "liveNow": False,
  "panels": [
    {
      "datasource": {
        "type": "victoriametrics-metrics-datasource",
        "uid": "${db_victoriametrics}"
      },
      "description": "1分間あたりの処理完了トラック数",
      "fieldConfig": {
        "defaults": {
          "color": {
            "mode": "thresholds"
          },
          "decimals": 1,
          "mappings": [],
          "thresholds": {
            "mode": "absolute",
            "steps": [
              {"color": "red", "value": None},
              {"color": "yellow", "value": 1},
              {"color": "green", "value": 3},
              {"color": "super-light-green", "value": 6}
            ]
          },
          "unit": "tracks/min"
        },
        "overrides": []
      },
      "gridPos": {
        "h": 4,
        "w": 4,
        "x": 0,
        "y": 0
      },
      "id": 1,
      "options": {
        "colorMode": "value",
        "graphMode": "area",
        "justifyMode": "auto",
        "orientation": "auto",
        "percentChangeColorMode": "standard",
        "reduceOptions": {
          "calcs": [
            "lastNotNull"
          ],
          "fields": "",
          "values": False
        },
        "showPercentChange": False,
        "textMode": "value",
        "wideLayout": True
      },
      "pluginVersion": "10.0.0",
      "targets": [
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_tasks_per_minute",
          "format": "time_series",
          "range": True,
          "refId": "A"
        }
      ],
      "title": "🎵 処理速度 (Tracks/min)",
      "type": "stat"
    },
    {
      "datasource": {
        "type": "victoriametrics-metrics-datasource",
        "uid": "${db_victoriametrics}"
      },
      "description": "1トラックあたりの平均所要時間 (EMA集約)",
      "fieldConfig": {
        "defaults": {
          "color": {
            "mode": "thresholds"
          },
          "decimals": 1,
          "mappings": [],
          "thresholds": {
            "mode": "absolute",
            "steps": [
              {"color": "green", "value": None},
              {"color": "yellow", "value": 180},
              {"color": "orange", "value": 300},
              {"color": "red", "value": 600}
            ]
          },
          "unit": "s"
        },
        "overrides": []
      },
      "gridPos": {
        "h": 4,
        "w": 4,
        "x": 4,
        "y": 0
      },
      "id": 2,
      "options": {
        "colorMode": "value",
        "graphMode": "area",
        "justifyMode": "auto",
        "orientation": "auto",
        "percentChangeColorMode": "standard",
        "reduceOptions": {
          "calcs": [
            "lastNotNull"
          ],
          "fields": "",
          "values": False
        },
        "showPercentChange": False,
        "textMode": "value",
        "wideLayout": True
      },
      "pluginVersion": "10.0.0",
      "targets": [
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_avg_task_duration_seconds",
          "format": "time_series",
          "range": True,
          "refId": "A"
        }
      ],
      "title": "⏱️ 1曲平均所要時間",
      "type": "stat"
    },
    {
      "datasource": {
        "type": "victoriametrics-metrics-datasource",
        "uid": "${db_victoriametrics}"
      },
      "description": "キュー全件完了までの残り推定時間",
      "fieldConfig": {
        "defaults": {
          "color": {
            "mode": "continuous-GrYlRd"
          },
          "decimals": 0,
          "mappings": [],
          "thresholds": {
            "mode": "absolute",
            "steps": [
              {"color": "green", "value": None},
              {"color": "yellow", "value": 36000},
              {"color": "red", "value": 180000}
            ]
          },
          "unit": "s"
        },
        "overrides": []
      },
      "gridPos": {
        "h": 4,
        "w": 4,
        "x": 8,
        "y": 0
      },
      "id": 3,
      "options": {
        "colorMode": "value",
        "graphMode": "area",
        "justifyMode": "auto",
        "orientation": "auto",
        "percentChangeColorMode": "standard",
        "reduceOptions": {
          "calcs": [
            "lastNotNull"
          ],
          "fields": "",
          "values": False
        },
        "showPercentChange": False,
        "textMode": "value",
        "wideLayout": True
      },
      "pluginVersion": "10.0.0",
      "targets": [
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_eta_seconds",
          "format": "time_series",
          "range": True,
          "refId": "A"
        }
      ],
      "title": "⏳ 残り推定時間 (ETA)",
      "type": "stat"
    },
    {
      "datasource": {
        "type": "victoriametrics-metrics-datasource",
        "uid": "${db_victoriametrics}"
      },
      "description": "未処理のトラックキュー残量",
      "fieldConfig": {
        "defaults": {
          "color": {
            "mode": "thresholds"
          },
          "decimals": 0,
          "mappings": [],
          "thresholds": {
            "mode": "absolute",
            "steps": [
              {"color": "blue", "value": None},
              {"color": "purple", "value": 500},
              {"color": "orange", "value": 1000}
            ]
          },
          "unit": "short"
        },
        "overrides": []
      },
      "gridPos": {
        "h": 4,
        "w": 4,
        "x": 12,
        "y": 0
      },
      "id": 4,
      "options": {
        "colorMode": "value",
        "graphMode": "area",
        "justifyMode": "auto",
        "orientation": "auto",
        "percentChangeColorMode": "standard",
        "reduceOptions": {
          "calcs": [
            "lastNotNull"
          ],
          "fields": "",
          "values": False
        },
        "showPercentChange": False,
        "textMode": "value",
        "wideLayout": True
      },
      "pluginVersion": "10.0.0",
      "targets": [
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_queue_length",
          "format": "time_series",
          "range": True,
          "refId": "A"
        }
      ],
      "title": "📋 残りキュー件数",
      "type": "stat"
    },
    {
      "datasource": {
        "type": "victoriametrics-metrics-datasource",
        "uid": "${db_victoriametrics}"
      },
      "description": "現在アクティブなワーカープロセス数およびDemucs専有枠",
      "fieldConfig": {
        "defaults": {
          "color": {
            "mode": "thresholds"
          },
          "decimals": 0,
          "mappings": [],
          "thresholds": {
            "mode": "absolute",
            "steps": [
              {"color": "blue", "value": None},
              {"color": "green", "value": 1},
              {"color": "yellow", "value": 8},
              {"color": "orange", "value": 16}
            ]
          },
          "unit": "short"
        },
        "overrides": []
      },
      "gridPos": {
        "h": 4,
        "w": 4,
        "x": 16,
        "y": 0
      },
      "id": 5,
      "options": {
        "colorMode": "value",
        "graphMode": "area",
        "justifyMode": "auto",
        "orientation": "auto",
        "percentChangeColorMode": "standard",
        "reduceOptions": {
          "calcs": [
            "lastNotNull"
          ],
          "fields": "",
          "values": False
        },
        "showPercentChange": False,
        "textMode": "value",
        "wideLayout": True
      },
      "pluginVersion": "10.0.0",
      "targets": [
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_active_workers",
          "format": "time_series",
          "legendFormat": "Workers",
          "range": True,
          "refId": "A"
        },
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_demucs_slots_in_use",
          "format": "time_series",
          "legendFormat": "Demucs Slot",
          "range": True,
          "refId": "B"
        }
      ],
      "title": "⚙️ 稼働ワーカー / Demucs枠",
      "type": "stat"
    },
    {
      "datasource": {
        "type": "victoriametrics-metrics-datasource",
        "uid": "${db_victoriametrics}"
      },
      "description": "解析完了トラック総数 (成功 vs エラー)",
      "fieldConfig": {
        "defaults": {
          "color": {
            "mode": "palette-classic"
          },
          "decimals": 0,
          "mappings": [],
          "thresholds": {
            "mode": "absolute",
            "steps": [
              {"color": "green", "value": None}
            ]
          },
          "unit": "short"
        },
        "overrides": [
          {
            "matcher": {
              "id": "byRegexp",
              "options": "/エラー/"
            },
            "properties": [
              {
                "id": "color",
                "value": {
                  "fixedColor": "red",
                  "mode": "fixed"
                }
              }
            ]
          }
        ]
      },
      "gridPos": {
        "h": 4,
        "w": 4,
        "x": 20,
        "y": 0
      },
      "id": 6,
      "options": {
        "colorMode": "value",
        "graphMode": "area",
        "justifyMode": "auto",
        "orientation": "auto",
        "percentChangeColorMode": "standard",
        "reduceOptions": {
          "calcs": [
            "lastNotNull"
          ],
          "fields": "",
          "values": False
        },
        "showPercentChange": False,
        "textMode": "value",
        "wideLayout": True
      },
      "pluginVersion": "10.0.0",
      "targets": [
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "sum(analyzer_tasks_total{status=\"success\"})",
          "format": "time_series",
          "legendFormat": "成功",
          "range": True,
          "refId": "A"
        },
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "sum(analyzer_tasks_total{status=\"error\"})",
          "format": "time_series",
          "legendFormat": "エラー",
          "range": True,
          "refId": "B"
        }
      ],
      "title": "✅ 完了トラック実績",
      "type": "stat"
    },
    {
      "datasource": {
        "type": "victoriametrics-metrics-datasource",
        "uid": "${db_victoriametrics}"
      },
      "description": "スループット (左軸) および 1曲所要時間 (右軸) の時系列推移",
      "fieldConfig": {
        "defaults": {
          "color": {
            "mode": "palette-classic"
          },
          "custom": {
            "axisBorderShow": False,
            "axisCenteredZero": False,
            "axisColorMode": "text",
            "axisLabel": "処理数 / 分",
            "axisPlacement": "left",
            "drawStyle": "line",
            "fillOpacity": 12,
            "gradientMode": "opacity",
            "lineWidth": 2,
            "pointSize": 5,
            "showPoints": "auto",
            "spanNulls": True,
            "stacking": {
              "group": "A",
              "mode": "none"
            }
          },
          "mappings": [],
          "thresholds": {
            "mode": "absolute",
            "steps": [
              {"color": "green", "value": None}
            ]
          },
          "unit": "short"
        },
        "overrides": [
          {
            "matcher": {
              "id": "byRegexp",
              "options": "/所要時間/"
            },
            "properties": [
              {
                "id": "custom.axisPlacement",
                "value": "right"
              },
              {
                "id": "custom.axisLabel",
                "value": "所要時間 (秒)"
              },
              {
                "id": "unit",
                "value": "s"
              },
              {
                "id": "custom.lineStyle",
                "value": {
                  "dash": [8, 6],
                  "fill": "dash"
                }
              }
            ]
          }
        ]
      },
      "gridPos": {
        "h": 8,
        "w": 12,
        "x": 0,
        "y": 4
      },
      "id": 10,
      "options": {
        "legend": {
          "calcs": [
            "lastNotNull",
            "mean",
            "max"
          ],
          "displayMode": "table",
          "placement": "bottom",
          "showLegend": True
        },
        "tooltip": {
          "mode": "multi",
          "sort": "desc"
        }
      },
      "pluginVersion": "10.0.0",
      "targets": [
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_tasks_per_minute",
          "format": "time_series",
          "legendFormat": "処理速度 (tracks/min)",
          "range": True,
          "refId": "A"
        },
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_files_per_minute",
          "format": "time_series",
          "legendFormat": "処理速度 (files/min)",
          "range": True,
          "refId": "B"
        },
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_avg_task_duration_seconds",
          "format": "time_series",
          "legendFormat": "1曲平均所要時間 (s)",
          "range": True,
          "refId": "C"
        },
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_last_task_duration_seconds",
          "format": "time_series",
          "legendFormat": "1曲直近所要時間 (s)",
          "range": True,
          "refId": "D"
        }
      ],
      "title": "📈 処理速度 ＆ 所要時間推移 (Throughput & Latency)",
      "type": "timeseries"
    },
    {
      "datasource": {
        "type": "victoriametrics-metrics-datasource",
        "uid": "${db_victoriametrics}"
      },
      "description": "各解析ステージごとの所要時間分解 (ボトルネックの特定)",
      "fieldConfig": {
        "defaults": {
          "color": {
            "mode": "palette-classic"
          },
          "custom": {
            "axisBorderShow": False,
            "axisCenteredZero": False,
            "axisColorMode": "text",
            "axisLabel": "所要時間",
            "axisPlacement": "left",
            "drawStyle": "line",
            "fillOpacity": 15,
            "gradientMode": "opacity",
            "lineWidth": 2,
            "pointSize": 5,
            "showPoints": "auto",
            "spanNulls": True,
            "stacking": {
              "group": "A",
              "mode": "none"
            }
          },
          "mappings": [],
          "thresholds": {
            "mode": "absolute",
            "steps": [
              {"color": "green", "value": None}
            ]
          },
          "unit": "s"
        },
        "overrides": [
          {
            "matcher": {"id": "byRegexp", "options": "/demucs/"},
            "properties": [{"id": "color", "value": {"fixedColor": "#E02F44", "mode": "fixed"}}]
          },
          {
            "matcher": {"id": "byRegexp", "options": "/librosa/"},
            "properties": [{"id": "color", "value": {"fixedColor": "#FF780A", "mode": "fixed"}}]
          },
          {
            "matcher": {"id": "byRegexp", "options": "/tensor/"},
            "properties": [{"id": "color", "value": {"fixedColor": "#56A64B", "mode": "fixed"}}]
          },
          {
            "matcher": {"id": "byRegexp", "options": "/essentia/"},
            "properties": [{"id": "color", "value": {"fixedColor": "#73BF69", "mode": "fixed"}}]
          },
          {
            "matcher": {"id": "byRegexp", "options": "/flac_tagger/"},
            "properties": [{"id": "color", "value": {"fixedColor": "#3274D9", "mode": "fixed"}}]
          },
          {
            "matcher": {"id": "byRegexp", "options": "/db_ingest/"},
            "properties": [{"id": "color", "value": {"fixedColor": "#8AB8FF", "mode": "fixed"}}]
          }
        ]
      },
      "gridPos": {
        "h": 8,
        "w": 12,
        "x": 12,
        "y": 4
      },
      "id": 11,
      "options": {
        "legend": {
          "calcs": [
            "lastNotNull",
            "mean",
            "max"
          ],
          "displayMode": "table",
          "placement": "bottom",
          "showLegend": True,
          "sortBy": "Last *",
          "sortDesc": True
        },
        "tooltip": {
          "mode": "multi",
          "sort": "desc"
        }
      },
      "pluginVersion": "10.0.0",
      "targets": [
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_last_stage_duration_seconds{stage=~\"demucs|librosa|tensor|essentia|flac_tagger|db_ingest|hash_check|shm_alloc\"}",
          "format": "time_series",
          "legendFormat": "{{stage}} (直近)",
          "range": True,
          "refId": "A"
        }
      ],
      "title": "🔍 ステージ別所要時間 (Stage Latency Breakdown)",
      "type": "timeseries"
    },
    {
      "datasource": {
        "type": "victoriametrics-metrics-datasource",
        "uid": "${db_victoriametrics}"
      },
      "description": "Demucs 同時実行制限に伴うセマフォ待機時間 (左軸) および待機中ワーカー数 (右軸)",
      "fieldConfig": {
        "defaults": {
          "color": {
            "mode": "palette-classic"
          },
          "custom": {
            "axisBorderShow": False,
            "axisCenteredZero": False,
            "axisColorMode": "text",
            "axisLabel": "待機時間",
            "axisPlacement": "left",
            "drawStyle": "line",
            "fillOpacity": 15,
            "gradientMode": "opacity",
            "lineWidth": 2,
            "pointSize": 5,
            "showPoints": "auto",
            "spanNulls": True,
            "stacking": {
              "group": "A",
              "mode": "none"
            }
          },
          "mappings": [],
          "thresholds": {
            "mode": "absolute",
            "steps": [
              {"color": "green", "value": None}
            ]
          },
          "unit": "s"
        },
        "overrides": [
          {
            "matcher": {
              "id": "byRegexp",
              "options": "/待機ワーカー数/"
            },
            "properties": [
              {
                "id": "custom.axisPlacement",
                "value": "right"
              },
              {
                "id": "custom.axisLabel",
                "value": "待機ワーカー数"
              },
              {
                "id": "unit",
                "value": "workers"
              },
              {
                "id": "custom.drawStyle",
                "value": "bars"
              },
              {
                "id": "custom.fillOpacity",
                "value": 30
              }
            ]
          }
        ]
      },
      "gridPos": {
        "h": 8,
        "w": 12,
        "x": 0,
        "y": 12
      },
      "id": 20,
      "options": {
        "legend": {
          "calcs": [
            "lastNotNull",
            "mean",
            "max"
          ],
          "displayMode": "table",
          "placement": "bottom",
          "showLegend": True,
          "sortBy": "Last *",
          "sortDesc": True
        },
        "tooltip": {
          "mode": "multi",
          "sort": "desc"
        }
      },
      "pluginVersion": "10.0.0",
      "targets": [
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_last_demucs_wait_seconds",
          "format": "time_series",
          "legendFormat": "Demucs 実行枠待ち時間 (s)",
          "range": True,
          "refId": "A"
        },
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_demucs_queue_waiters",
          "format": "time_series",
          "legendFormat": "Demucs 待機ワーカー数 (workers)",
          "range": True,
          "refId": "B"
        },
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_last_gatekeeper_wait_seconds",
          "format": "time_series",
          "legendFormat": "Gatekeeper 防御待機時間 (s)",
          "range": True,
          "refId": "C"
        }
      ],
      "title": "⏳ リソース競合 ＆ 待機時間 (Contention & Queue Wait)",
      "type": "timeseries"
    },
    {
      "datasource": {
        "type": "victoriametrics-metrics-datasource",
        "uid": "${db_victoriametrics}"
      },
      "description": "Python サブプロセス内部のステップ別所要時間 (Warmup vs Extract vs Inference)",
      "fieldConfig": {
        "defaults": {
          "color": {
            "mode": "palette-classic"
          },
          "custom": {
            "axisBorderShow": False,
            "axisCenteredZero": False,
            "axisColorMode": "text",
            "axisLabel": "所要時間",
            "axisPlacement": "left",
            "drawStyle": "line",
            "fillOpacity": 10,
            "gradientMode": "none",
            "lineWidth": 1.5,
            "pointSize": 5,
            "showPoints": "never",
            "spanNulls": True,
            "stacking": {
              "group": "A",
              "mode": "none"
            }
          },
          "mappings": [],
          "thresholds": {
            "mode": "absolute",
            "steps": [
              {"color": "green", "value": None}
            ]
          },
          "unit": "s"
        },
        "overrides": []
      },
      "gridPos": {
        "h": 8,
        "w": 12,
        "x": 12,
        "y": 12
      },
      "id": 21,
      "options": {
        "legend": {
          "calcs": [
            "lastNotNull",
            "mean",
            "max"
          ],
          "displayMode": "table",
          "placement": "bottom",
          "showLegend": True,
          "sortBy": "Last *",
          "sortDesc": True
        },
        "tooltip": {
          "mode": "multi",
          "sort": "desc"
        }
      },
      "pluginVersion": "10.0.0",
      "targets": [
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_python_last_stage_duration_seconds",
          "format": "time_series",
          "legendFormat": "{{component}} - {{step}}",
          "range": True,
          "refId": "A"
        }
      ],
      "title": "🐍 Python 内部ステッププロファイル (Step Breakdown)",
      "type": "timeseries"
    },
    {
      "datasource": {
        "type": "victoriametrics-metrics-datasource",
        "uid": "${db_victoriametrics}"
      },
      "description": "物理RAM・ディスク空き容量およびプロセスメモリ推移",
      "fieldConfig": {
        "defaults": {
          "color": {
            "mode": "palette-classic"
          },
          "custom": {
            "axisBorderShow": False,
            "axisCenteredZero": False,
            "axisColorMode": "text",
            "axisLabel": "容量",
            "axisPlacement": "left",
            "drawStyle": "line",
            "fillOpacity": 10,
            "gradientMode": "opacity",
            "lineWidth": 1.5,
            "pointSize": 5,
            "showPoints": "never",
            "spanNulls": True,
            "stacking": {
              "group": "A",
              "mode": "none"
            }
          },
          "mappings": [],
          "thresholds": {
            "mode": "absolute",
            "steps": [
              {"color": "green", "value": None}
            ]
          },
          "unit": "bytes"
        },
        "overrides": []
      },
      "gridPos": {
        "h": 8,
        "w": 8,
        "x": 0,
        "y": 20
      },
      "id": 30,
      "options": {
        "legend": {
          "calcs": [
            "lastNotNull",
            "min"
          ],
          "displayMode": "table",
          "placement": "bottom",
          "showLegend": True
        },
        "tooltip": {
          "mode": "multi",
          "sort": "desc"
        }
      },
      "pluginVersion": "10.0.0",
      "targets": [
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_ram_available_bytes",
          "format": "time_series",
          "legendFormat": "システム空き RAM",
          "range": True,
          "refId": "A"
        },
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "process_resident_memory_bytes",
          "format": "time_series",
          "legendFormat": "Orchestrator 常駐メモリ (RSS)",
          "range": True,
          "refId": "B"
        },
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_disk_free_bytes",
          "format": "time_series",
          "legendFormat": "作業ディスク空き容量",
          "range": True,
          "refId": "C"
        }
      ],
      "title": "💾 システムメモリ ＆ ディスク容量",
      "type": "timeseries"
    },
    {
      "datasource": {
        "type": "victoriametrics-metrics-datasource",
        "uid": "${db_victoriametrics}"
      },
      "description": "Orchestrator CPU使用率 (左軸) および Goroutines / Threads (右軸)",
      "fieldConfig": {
        "defaults": {
          "color": {
            "mode": "palette-classic"
          },
          "custom": {
            "axisBorderShow": False,
            "axisCenteredZero": False,
            "axisColorMode": "text",
            "axisLabel": "CPU %",
            "axisPlacement": "left",
            "drawStyle": "line",
            "fillOpacity": 8,
            "gradientMode": "none",
            "lineWidth": 1.5,
            "pointSize": 5,
            "showPoints": "never",
            "spanNulls": True,
            "stacking": {
              "group": "A",
              "mode": "none"
            }
          },
          "mappings": [],
          "thresholds": {
            "mode": "absolute",
            "steps": [
              {"color": "green", "value": None}
            ]
          },
          "unit": "percent"
        },
        "overrides": [
          {
            "matcher": {
              "id": "byRegexp",
              "options": "/Goroutines|Threads/"
            },
            "properties": [
              {
                "id": "custom.axisPlacement",
                "value": "right"
              },
              {
                "id": "custom.axisLabel",
                "value": "Count"
              },
              {
                "id": "unit",
                "value": "short"
              }
            ]
          }
        ]
      },
      "gridPos": {
        "h": 8,
        "w": 8,
        "x": 8,
        "y": 20
      },
      "id": 31,
      "options": {
        "legend": {
          "calcs": [
            "lastNotNull",
            "mean",
            "max"
          ],
          "displayMode": "table",
          "placement": "bottom",
          "showLegend": True
        },
        "tooltip": {
          "mode": "multi",
          "sort": "desc"
        }
      },
      "pluginVersion": "10.0.0",
      "targets": [
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "rate(process_cpu_seconds_total[1m]) * 100",
          "format": "time_series",
          "legendFormat": "Orchestrator CPU使用率 (%)",
          "range": True,
          "refId": "A"
        },
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "go_goroutines",
          "format": "time_series",
          "legendFormat": "Goroutines",
          "range": True,
          "refId": "B"
        },
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "go_threads",
          "format": "time_series",
          "legendFormat": "OS Threads",
          "range": True,
          "refId": "C"
        }
      ],
      "title": "⚡ プロセス CPU負荷 ＆ Goroutines",
      "type": "timeseries"
    },
    {
      "datasource": {
        "type": "victoriametrics-metrics-datasource",
        "uid": "${db_victoriametrics}"
      },
      "description": "各ステージが占める平均所要時間の構成比率",
      "fieldConfig": {
        "defaults": {
          "color": {
            "mode": "palette-classic"
          },
          "mappings": [],
          "unit": "s"
        },
        "overrides": []
      },
      "gridPos": {
        "h": 8,
        "w": 8,
        "x": 16,
        "y": 20
      },
      "id": 32,
      "options": {
        "displayLabels": [
          "percent"
        ],
        "legend": {
          "displayMode": "table",
          "placement": "right",
          "showLegend": True,
          "values": [
            "value",
            "percent"
          ]
        },
        "pieType": "donut",
        "reduceOptions": {
          "calcs": [
            "lastNotNull"
          ],
          "fields": "",
          "values": False
        },
        "sort": "desc",
        "tooltip": {
          "mode": "multi",
          "sort": "none"
        }
      },
      "pluginVersion": "10.0.0",
      "targets": [
        {
          "datasource": {
            "type": "victoriametrics-metrics-datasource",
            "uid": "${db_victoriametrics}"
          },
          "editorMode": "code",
          "expr": "analyzer_avg_stage_duration_seconds{stage=~\"demucs|librosa|tensor|essentia|flac_tagger|db_ingest|hash_check|shm_alloc\"} > 0",
          "format": "time_series",
          "instant": True,
          "legendFormat": "{{stage}}",
          "range": False,
          "refId": "A"
        }
      ],
      "title": "🍰 ステージ別平均処理時間比率",
      "type": "piechart"
    },
    {
      "datasource": {
        "type": "loki",
        "uid": "${db_loki}"
      },
      "description": "Orchestrator および各ワーカープロセスのリアルタイムログ",
      "fieldConfig": {
        "defaults": {},
        "overrides": []
      },
      "gridPos": {
        "h": 8,
        "w": 24,
        "x": 0,
        "y": 28
      },
      "id": 40,
      "options": {
        "dedupStrategy": "none",
        "enableInfiniteScrolling": True,
        "enableLogDetails": True,
        "fontSize": "small",
        "prettifyLogMessage": True,
        "showControls": True,
        "showFieldSelector": False,
        "showLabels": False,
        "showLevel": True,
        "showTime": True,
        "sortOrder": "Descending",
        "syntaxHighlighting": True,
        "timestampResolution": "ms",
        "unwrappedColumns": True,
        "wrapLogMessage": False
      },
      "pluginVersion": "10.0.0",
      "targets": [
        {
          "datasource": {
            "type": "loki",
            "uid": "${db_loki}"
          },
          "direction": "backward",
          "editorMode": "code",
          "expr": "{source=~\"(?i).*flac.*|.*analyzer.*|.*orchestrator.*\"} |= \"\" | json | label_format computer=\"{{.computer}}\", level=\"{{.level}}\", message=\"{{.message}}\"",
          "legendFormat": "computer, level, message",
          "queryType": "range",
          "refId": "A"
        }
      ],
      "title": "📋 Analyzer 実行ログ (Loki Stream)",
      "type": "logs"
    }
  ],
  "refresh": "10s",
  "schemaVersion": 38,
  "style": "dark",
  "tags": [
    "flac-analyzer",
    "orchestrator",
    "audio",
    "demucs",
    "victoriametrics",
    "loki"
  ],
  "templating": {
    "list": [
      {
        "current": {
          "selected": True,
          "text": "victoriametrics-metrics-datasource",
          "value": "dfptek8zvsgzkd"
        },
        "hide": 0,
        "includeAll": False,
        "label": "db_victoriametrics",
        "multi": False,
        "name": "db_victoriametrics",
        "options": [],
        "query": "victoriametrics-metrics-datasource",
        "refresh": 1,
        "regex": "",
        "skipUrlSync": False,
        "type": "datasource"
      },
      {
        "current": {
          "selected": True,
          "text": "loki",
          "value": "efptf4f0yu0hsf"
        },
        "hide": 0,
        "includeAll": False,
        "label": "db_loki",
        "multi": False,
        "name": "db_loki",
        "options": [],
        "query": "loki",
        "refresh": 1,
        "regex": "",
        "skipUrlSync": False,
        "type": "datasource"
      }
    ]
  },
  "time": {
    "from": "now-3h",
    "to": "now"
  },
  "timepicker": {
    "refresh_intervals": [
      "5s",
      "10s",
      "30s",
      "1m",
      "5m",
      "15m"
    ]
  },
  "timezone": "Asia/Tokyo",
  "title": "🎵 FLAC Analyzer - パイプライン監視 ＆ ボトルネック分析",
  "uid": "flac-analyzer-pipeline",
  "version": 1,
  "weekStart": ""
}

out_path = os.path.join("dashboards", "flac_analyzer_dashboard.json")
with open(out_path, "w", encoding="utf-8") as f:
    json.dump(dashboard_json_model, f, ensure_ascii=False, indent=2)

print(f"Generated perfect JSON Model at {out_path} ({len(json.dumps(dashboard_json_model))} bytes)")
