import json
import os

dashboard = {
  "apiVersion": "dashboard.grafana.app/v2",
  "kind": "Dashboard",
  "metadata": {
    "name": "flac-analyzer-pipeline"
  },
  "spec": {
    "annotations": [
      {
        "kind": "AnnotationQuery",
        "spec": {
          "builtIn": True,
          "enable": True,
          "hide": True,
          "iconColor": "rgba(0, 211, 255, 1)",
          "name": "Annotations & Alerts",
          "query": {
            "datasource": {
              "name": "-- Grafana --"
            },
            "group": "grafana",
            "kind": "DataQuery",
            "spec": {},
            "version": "v0"
          }
        }
      }
    ],
    "cursorSync": "Crosshair",
    "description": "FLAC Analyzer / Win32 Orchestrator 音響解析パイプライン監視 ＆ ボトルネック可観測性ダッシュボード",
    "editable": True,
    "elements": {
      "panel-1": {
        "kind": "Panel",
        "spec": {
          "data": {
            "kind": "QueryGroup",
            "spec": {
              "queries": [
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_tasks_per_minute",
                        "format": "time_series",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "A"
                  }
                }
              ],
              "queryOptions": {},
              "transformations": []
            }
          },
          "description": "1分間あたりの処理完了トラック数",
          "id": 1,
          "links": [],
          "title": "🎵 処理速度 (Tracks/min)",
          "vizConfig": {
            "group": "stat",
            "kind": "VizConfig",
            "spec": {
              "fieldConfig": {
                "defaults": {
                  "color": {
                    "mode": "thresholds"
                  },
                  "decimals": 1,
                  "thresholds": {
                    "mode": "absolute",
                    "steps": [
                      {"color": "red", "value": 0},
                      {"color": "yellow", "value": 1},
                      {"color": "green", "value": 3},
                      {"color": "super-light-green", "value": 6}
                    ]
                  },
                  "unit": "tracks/min"
                },
                "overrides": []
              },
              "options": {
                "colorMode": "value",
                "graphMode": "area",
                "justifyMode": "auto",
                "orientation": "auto",
                "percentChangeColorMode": "standard",
                "reduceOptions": {
                  "calcs": ["lastNotNull"],
                  "fields": "",
                  "values": False
                },
                "showPercentChange": False,
                "textMode": "value",
                "wideLayout": True
              }
            },
            "version": "13.1.1"
          }
        }
      },
      "panel-2": {
        "kind": "Panel",
        "spec": {
          "data": {
            "kind": "QueryGroup",
            "spec": {
              "queries": [
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_avg_task_duration_seconds",
                        "format": "time_series",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "A"
                  }
                }
              ],
              "queryOptions": {},
              "transformations": []
            }
          },
          "description": "1トラックあたりの平均所要時間 (EMA集約)",
          "id": 2,
          "links": [],
          "title": "⏱️ 1曲平均所要時間",
          "vizConfig": {
            "group": "stat",
            "kind": "VizConfig",
            "spec": {
              "fieldConfig": {
                "defaults": {
                  "color": {
                    "mode": "thresholds"
                  },
                  "decimals": 1,
                  "thresholds": {
                    "mode": "absolute",
                    "steps": [
                      {"color": "green", "value": 0},
                      {"color": "yellow", "value": 180},
                      {"color": "orange", "value": 300},
                      {"color": "red", "value": 600}
                    ]
                  },
                  "unit": "s"
                },
                "overrides": []
              },
              "options": {
                "colorMode": "value",
                "graphMode": "area",
                "justifyMode": "auto",
                "orientation": "auto",
                "percentChangeColorMode": "standard",
                "reduceOptions": {
                  "calcs": ["lastNotNull"],
                  "fields": "",
                  "values": False
                },
                "showPercentChange": False,
                "textMode": "value",
                "wideLayout": True
              }
            },
            "version": "13.1.1"
          }
        }
      },
      "panel-3": {
        "kind": "Panel",
        "spec": {
          "data": {
            "kind": "QueryGroup",
            "spec": {
              "queries": [
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_eta_seconds",
                        "format": "time_series",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "A"
                  }
                }
              ],
              "queryOptions": {},
              "transformations": []
            }
          },
          "description": "キュー全件完了までの残り推定時間",
          "id": 3,
          "links": [],
          "title": "⏳ 残り推定時間 (ETA)",
          "vizConfig": {
            "group": "stat",
            "kind": "VizConfig",
            "spec": {
              "fieldConfig": {
                "defaults": {
                  "color": {
                    "mode": "continuous-GrYlRd"
                  },
                  "decimals": 0,
                  "thresholds": {
                    "mode": "absolute",
                    "steps": [
                      {"color": "green", "value": 0},
                      {"color": "yellow", "value": 36000},
                      {"color": "red", "value": 180000}
                    ]
                  },
                  "unit": "s"
                },
                "overrides": []
              },
              "options": {
                "colorMode": "value",
                "graphMode": "area",
                "justifyMode": "auto",
                "orientation": "auto",
                "percentChangeColorMode": "standard",
                "reduceOptions": {
                  "calcs": ["lastNotNull"],
                  "fields": "",
                  "values": False
                },
                "showPercentChange": False,
                "textMode": "value",
                "wideLayout": True
              }
            },
            "version": "13.1.1"
          }
        }
      },
      "panel-4": {
        "kind": "Panel",
        "spec": {
          "data": {
            "kind": "QueryGroup",
            "spec": {
              "queries": [
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_queue_length",
                        "format": "time_series",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "A"
                  }
                }
              ],
              "queryOptions": {},
              "transformations": []
            }
          },
          "description": "未処理のトラックキュー残量",
          "id": 4,
          "links": [],
          "title": "📋 残りキュー件数",
          "vizConfig": {
            "group": "stat",
            "kind": "VizConfig",
            "spec": {
              "fieldConfig": {
                "defaults": {
                  "color": {
                    "mode": "thresholds"
                  },
                  "decimals": 0,
                  "thresholds": {
                    "mode": "absolute",
                    "steps": [
                      {"color": "blue", "value": 0},
                      {"color": "purple", "value": 500},
                      {"color": "orange", "value": 1000}
                    ]
                  },
                  "unit": "short"
                },
                "overrides": []
              },
              "options": {
                "colorMode": "value",
                "graphMode": "area",
                "justifyMode": "auto",
                "orientation": "auto",
                "percentChangeColorMode": "standard",
                "reduceOptions": {
                  "calcs": ["lastNotNull"],
                  "fields": "",
                  "values": False
                },
                "showPercentChange": False,
                "textMode": "value",
                "wideLayout": True
              }
            },
            "version": "13.1.1"
          }
        }
      },
      "panel-5": {
        "kind": "Panel",
        "spec": {
          "data": {
            "kind": "QueryGroup",
            "spec": {
              "queries": [
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_active_workers",
                        "format": "time_series",
                        "legendFormat": "Workers",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "A"
                  }
                },
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_demucs_slots_in_use",
                        "format": "time_series",
                        "legendFormat": "Demucs Slot",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "B"
                  }
                }
              ],
              "queryOptions": {},
              "transformations": []
            }
          },
          "description": "現在アクティブなワーカープロセス数およびDemucs専有枠",
          "id": 5,
          "links": [],
          "title": "⚙️ 稼働ワーカー / Demucs枠",
          "vizConfig": {
            "group": "stat",
            "kind": "VizConfig",
            "spec": {
              "fieldConfig": {
                "defaults": {
                  "color": {
                    "mode": "thresholds"
                  },
                  "decimals": 0,
                  "thresholds": {
                    "mode": "absolute",
                    "steps": [
                      {"color": "blue", "value": 0},
                      {"color": "green", "value": 1},
                      {"color": "yellow", "value": 8},
                      {"color": "orange", "value": 16}
                    ]
                  },
                  "unit": "short"
                },
                "overrides": []
              },
              "options": {
                "colorMode": "value",
                "graphMode": "area",
                "justifyMode": "auto",
                "orientation": "auto",
                "percentChangeColorMode": "standard",
                "reduceOptions": {
                  "calcs": ["lastNotNull"],
                  "fields": "",
                  "values": False
                },
                "showPercentChange": False,
                "textMode": "value",
                "wideLayout": True
              }
            },
            "version": "13.1.1"
          }
        }
      },
      "panel-6": {
        "kind": "Panel",
        "spec": {
          "data": {
            "kind": "QueryGroup",
            "spec": {
              "queries": [
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "sum(analyzer_tasks_total{status=\"success\"})",
                        "format": "time_series",
                        "legendFormat": "成功",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "A"
                  }
                },
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "sum(analyzer_tasks_total{status=\"error\"})",
                        "format": "time_series",
                        "legendFormat": "エラー",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "B"
                  }
                }
              ],
              "queryOptions": {},
              "transformations": []
            }
          },
          "description": "解析完了トラック総数 (成功 vs エラー)",
          "id": 6,
          "links": [],
          "title": "✅ 完了トラック実績",
          "vizConfig": {
            "group": "stat",
            "kind": "VizConfig",
            "spec": {
              "fieldConfig": {
                "defaults": {
                  "color": {
                    "mode": "palette-classic"
                  },
                  "decimals": 0,
                  "thresholds": {
                    "mode": "absolute",
                    "steps": [
                      {"color": "green", "value": 0}
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
              "options": {
                "colorMode": "value",
                "graphMode": "area",
                "justifyMode": "auto",
                "orientation": "auto",
                "percentChangeColorMode": "standard",
                "reduceOptions": {
                  "calcs": ["lastNotNull"],
                  "fields": "",
                  "values": False
                },
                "showPercentChange": False,
                "textMode": "value",
                "wideLayout": True
              }
            },
            "version": "13.1.1"
          }
        }
      },
      "panel-10": {
        "kind": "Panel",
        "spec": {
          "data": {
            "kind": "QueryGroup",
            "spec": {
              "queries": [
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_tasks_per_minute",
                        "format": "time_series",
                        "legendFormat": "処理速度 (tracks/min)",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "A"
                  }
                },
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_files_per_minute",
                        "format": "time_series",
                        "legendFormat": "処理速度 (files/min)",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "B"
                  }
                },
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_avg_task_duration_seconds",
                        "format": "time_series",
                        "legendFormat": "1曲平均所要時間 (s)",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "C"
                  }
                },
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_last_task_duration_seconds",
                        "format": "time_series",
                        "legendFormat": "1曲直近所要時間 (s)",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "D"
                  }
                }
              ],
              "queryOptions": {},
              "transformations": []
            }
          },
          "description": "スループット (左軸) および 1曲所要時間 (右軸) の時系列推移",
          "id": 10,
          "links": [],
          "title": "📈 処理速度 ＆ 所要時間推移 (Throughput & Latency)",
          "vizConfig": {
            "group": "timeseries",
            "kind": "VizConfig",
            "spec": {
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
              "options": {
                "legend": {
                  "calcs": ["lastNotNull", "mean", "max"],
                  "displayMode": "table",
                  "placement": "bottom",
                  "showLegend": True
                },
                "tooltip": {
                  "mode": "multi",
                  "sort": "desc"
                }
              }
            },
            "version": "13.1.1"
          }
        }
      },
      "panel-11": {
        "kind": "Panel",
        "spec": {
          "data": {
            "kind": "QueryGroup",
            "spec": {
              "queries": [
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_last_stage_duration_seconds{stage=~\"demucs|librosa|tensor|essentia|flac_tagger|db_ingest|hash_check|shm_alloc\"}",
                        "format": "time_series",
                        "legendFormat": "{{stage}} (直近)",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "A"
                  }
                }
              ],
              "queryOptions": {},
              "transformations": []
            }
          },
          "description": "各解析ステージごとの所要時間分解 (ボトルネックの特定)",
          "id": 11,
          "links": [],
          "title": "🔍 ステージ別所要時間 (Stage Latency Breakdown)",
          "vizConfig": {
            "group": "timeseries",
            "kind": "VizConfig",
            "spec": {
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
                  "unit": "s"
                },
                "overrides": [
                  {
                    "matcher": {
                      "id": "byRegexp",
                      "options": "/demucs/"
                    },
                    "properties": [
                      {"id": "color", "value": {"fixedColor": "#E02F44", "mode": "fixed"}}
                    ]
                  },
                  {
                    "matcher": {
                      "id": "byRegexp",
                      "options": "/librosa/"
                    },
                    "properties": [
                      {"id": "color", "value": {"fixedColor": "#FF780A", "mode": "fixed"}}
                    ]
                  },
                  {
                    "matcher": {
                      "id": "byRegexp",
                      "options": "/tensor/"
                    },
                    "properties": [
                      {"id": "color", "value": {"fixedColor": "#56A64B", "mode": "fixed"}}
                    ]
                  },
                  {
                    "matcher": {
                      "id": "byRegexp",
                      "options": "/essentia/"
                    },
                    "properties": [
                      {"id": "color", "value": {"fixedColor": "#73BF69", "mode": "fixed"}}
                    ]
                  },
                  {
                    "matcher": {
                      "id": "byRegexp",
                      "options": "/flac_tagger/"
                    },
                    "properties": [
                      {"id": "color", "value": {"fixedColor": "#3274D9", "mode": "fixed"}}
                    ]
                  },
                  {
                    "matcher": {
                      "id": "byRegexp",
                      "options": "/db_ingest/"
                    },
                    "properties": [
                      {"id": "color", "value": {"fixedColor": "#8AB8FF", "mode": "fixed"}}
                    ]
                  }
                ]
              },
              "options": {
                "legend": {
                  "calcs": ["lastNotNull", "mean", "max"],
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
              }
            },
            "version": "13.1.1"
          }
        }
      },
      "panel-20": {
        "kind": "Panel",
        "spec": {
          "data": {
            "kind": "QueryGroup",
            "spec": {
              "queries": [
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_last_demucs_wait_seconds",
                        "format": "time_series",
                        "legendFormat": "Demucs 実行枠待ち時間 (s)",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "A"
                  }
                },
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_demucs_queue_waiters",
                        "format": "time_series",
                        "legendFormat": "Demucs 待機ワーカー数 (workers)",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "B"
                  }
                },
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_last_gatekeeper_wait_seconds",
                        "format": "time_series",
                        "legendFormat": "Gatekeeper 防御待機時間 (s)",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "C"
                  }
                }
              ],
              "queryOptions": {},
              "transformations": []
            }
          },
          "description": "Demucs 同時実行制限に伴うセマフォ待機時間 (左軸) および待機中ワーカー数 (右軸)",
          "id": 20,
          "links": [],
          "title": "⏳ リソース競合 ＆ 待機時間 (Contention & Queue Wait)",
          "vizConfig": {
            "group": "timeseries",
            "kind": "VizConfig",
            "spec": {
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
              "options": {
                "legend": {
                  "calcs": ["lastNotNull", "mean", "max"],
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
              }
            },
            "version": "13.1.1"
          }
        }
      },
      "panel-21": {
        "kind": "Panel",
        "spec": {
          "data": {
            "kind": "QueryGroup",
            "spec": {
              "queries": [
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_python_last_stage_duration_seconds",
                        "format": "time_series",
                        "legendFormat": "{{component}} - {{step}}",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "A"
                  }
                }
              ],
              "queryOptions": {},
              "transformations": []
            }
          },
          "description": "Python サブプロセス内部のステップ別所要時間 (Warmup vs Extract vs Inference)",
          "id": 21,
          "links": [],
          "title": "🐍 Python 内部ステッププロファイル (Step Breakdown)",
          "vizConfig": {
            "group": "timeseries",
            "kind": "VizConfig",
            "spec": {
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
                  "unit": "s"
                },
                "overrides": []
              },
              "options": {
                "legend": {
                  "calcs": ["lastNotNull", "mean", "max"],
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
              }
            },
            "version": "13.1.1"
          }
        }
      },
      "panel-30": {
        "kind": "Panel",
        "spec": {
          "data": {
            "kind": "QueryGroup",
            "spec": {
              "queries": [
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_ram_available_bytes",
                        "format": "time_series",
                        "legendFormat": "システム空き RAM",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "A"
                  }
                },
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "process_resident_memory_bytes",
                        "format": "time_series",
                        "legendFormat": "Orchestrator 常駐メモリ (RSS)",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "B"
                  }
                },
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_disk_free_bytes",
                        "format": "time_series",
                        "legendFormat": "作業ディスク空き容量",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "C"
                  }
                }
              ],
              "queryOptions": {},
              "transformations": []
            }
          },
          "description": "物理RAM・ディスク空き容量およびプロセスメモリ推移",
          "id": 30,
          "links": [],
          "title": "💾 システムメモリ ＆ ディスク容量",
          "vizConfig": {
            "group": "timeseries",
            "kind": "VizConfig",
            "spec": {
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
                  "unit": "bytes"
                },
                "overrides": []
              },
              "options": {
                "legend": {
                  "calcs": ["lastNotNull", "min"],
                  "displayMode": "table",
                  "placement": "bottom",
                  "showLegend": True
                },
                "tooltip": {
                  "mode": "multi",
                  "sort": "desc"
                }
              }
            },
            "version": "13.1.1"
          }
        }
      },
      "panel-31": {
        "kind": "Panel",
        "spec": {
          "data": {
            "kind": "QueryGroup",
            "spec": {
              "queries": [
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "rate(process_cpu_seconds_total[1m]) * 100",
                        "format": "time_series",
                        "legendFormat": "Orchestrator CPU使用率 (%)",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "A"
                  }
                },
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "go_goroutines",
                        "format": "time_series",
                        "legendFormat": "Goroutines",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "B"
                  }
                },
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "go_threads",
                        "format": "time_series",
                        "legendFormat": "OS Threads",
                        "range": True
                      },
                      "version": "v0"
                    },
                    "refId": "C"
                  }
                }
              ],
              "queryOptions": {},
              "transformations": []
            }
          },
          "description": "Orchestrator CPU使用率 (左軸) および Goroutines / Threads (右軸)",
          "id": 31,
          "links": [],
          "title": "⚡ プロセス CPU負荷 ＆ Goroutines",
          "vizConfig": {
            "group": "timeseries",
            "kind": "VizConfig",
            "spec": {
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
              "options": {
                "legend": {
                  "calcs": ["lastNotNull", "mean", "max"],
                  "displayMode": "table",
                  "placement": "bottom",
                  "showLegend": True
                },
                "tooltip": {
                  "mode": "multi",
                  "sort": "desc"
                }
              }
            },
            "version": "13.1.1"
          }
        }
      },
      "panel-32": {
        "kind": "Panel",
        "spec": {
          "data": {
            "kind": "QueryGroup",
            "spec": {
              "queries": [
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_victoriametrics}"
                      },
                      "group": "victoriametrics-metrics-datasource",
                      "kind": "DataQuery",
                      "spec": {
                        "editorMode": "code",
                        "expr": "analyzer_avg_stage_duration_seconds{stage=~\"demucs|librosa|tensor|essentia|flac_tagger|db_ingest|hash_check|shm_alloc\"} > 0",
                        "format": "time_series",
                        "instant": True,
                        "legendFormat": "{{stage}}",
                        "range": False
                      },
                      "version": "v0"
                    },
                    "refId": "A"
                  }
                }
              ],
              "queryOptions": {},
              "transformations": []
            }
          },
          "description": "各ステージが占める平均所要時間の構成比率",
          "id": 32,
          "links": [],
          "title": "🍰 ステージ別平均処理時間比率",
          "vizConfig": {
            "group": "piechart",
            "kind": "VizConfig",
            "spec": {
              "fieldConfig": {
                "defaults": {
                  "color": {
                    "mode": "palette-classic"
                  },
                  "unit": "s"
                },
                "overrides": []
              },
              "options": {
                "displayLabels": ["percent"],
                "legend": {
                  "displayMode": "table",
                  "placement": "right",
                  "showLegend": True,
                  "values": ["value", "percent"]
                },
                "pieType": "donut",
                "reduceOptions": {
                  "calcs": ["lastNotNull"],
                  "fields": "",
                  "values": False
                },
                "sort": "desc",
                "tooltip": {
                  "mode": "multi",
                  "sort": "none"
                }
              }
            },
            "version": "13.1.1"
          }
        }
      },
      "panel-40": {
        "kind": "Panel",
        "spec": {
          "data": {
            "kind": "QueryGroup",
            "spec": {
              "queries": [
                {
                  "kind": "PanelQuery",
                  "spec": {
                    "hidden": False,
                    "query": {
                      "datasource": {
                        "name": "${db_loki}"
                      },
                      "group": "loki",
                      "kind": "DataQuery",
                      "spec": {
                        "direction": "backward",
                        "editorMode": "code",
                        "expr": "{source=~\"(?i).*flac.*|.*analyzer.*|.*orchestrator.*\"} |= \"\" | json | label_format computer=\"{{.computer}}\", level=\"{{.level}}\", message=\"{{.message}}\"",
                        "legendFormat": "computer, level, message",
                        "queryType": "range"
                      },
                      "version": "v0"
                    },
                    "refId": "A"
                  }
                }
              ],
              "queryOptions": {},
              "transformations": []
            }
          },
          "description": "Orchestrator および各ワーカープロセスのリアルタイムログ",
          "id": 40,
          "links": [],
          "title": "📋 Analyzer 実行ログ (Loki Stream)",
          "vizConfig": {
            "group": "logs",
            "kind": "VizConfig",
            "spec": {
              "fieldConfig": {
                "defaults": {},
                "overrides": []
              },
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
              }
            },
            "version": "13.1.1"
          }
        }
      }
    },
    "layout": {
      "kind": "GridLayout",
      "spec": {
        "items": [
          # Row 1: Top KPI Summary (Stat Panels) [y=0, h=4]
          {"kind": "GridLayoutItem", "spec": {"element": {"kind": "ElementReference", "name": "panel-1"}, "height": 4, "width": 4, "x": 0, "y": 0}},
          {"kind": "GridLayoutItem", "spec": {"element": {"kind": "ElementReference", "name": "panel-2"}, "height": 4, "width": 4, "x": 4, "y": 0}},
          {"kind": "GridLayoutItem", "spec": {"element": {"kind": "ElementReference", "name": "panel-3"}, "height": 4, "width": 4, "x": 8, "y": 0}},
          {"kind": "GridLayoutItem", "spec": {"element": {"kind": "ElementReference", "name": "panel-4"}, "height": 4, "width": 4, "x": 12, "y": 0}},
          {"kind": "GridLayoutItem", "spec": {"element": {"kind": "ElementReference", "name": "panel-5"}, "height": 4, "width": 4, "x": 16, "y": 0}},
          {"kind": "GridLayoutItem", "spec": {"element": {"kind": "ElementReference", "name": "panel-6"}, "height": 4, "width": 4, "x": 20, "y": 0}},

          # Row 2: Throughput & Stage Latency Breakdown [y=4, h=8]
          {"kind": "GridLayoutItem", "spec": {"element": {"kind": "ElementReference", "name": "panel-10"}, "height": 8, "width": 12, "x": 0, "y": 4}},
          {"kind": "GridLayoutItem", "spec": {"element": {"kind": "ElementReference", "name": "panel-11"}, "height": 8, "width": 12, "x": 12, "y": 4}},

          # Row 3: Contention / Queuing & Python Steps [y=12, h=8]
          {"kind": "GridLayoutItem", "spec": {"element": {"kind": "ElementReference", "name": "panel-20"}, "height": 8, "width": 12, "x": 0, "y": 12}},
          {"kind": "GridLayoutItem", "spec": {"element": {"kind": "ElementReference", "name": "panel-21"}, "height": 8, "width": 12, "x": 12, "y": 12}},

          # Row 4: System Resources & Process Diagnostics [y=20, h=8]
          {"kind": "GridLayoutItem", "spec": {"element": {"kind": "ElementReference", "name": "panel-30"}, "height": 8, "width": 8, "x": 0, "y": 20}},
          {"kind": "GridLayoutItem", "spec": {"element": {"kind": "ElementReference", "name": "panel-31"}, "height": 8, "width": 8, "x": 8, "y": 20}},
          {"kind": "GridLayoutItem", "spec": {"element": {"kind": "ElementReference", "name": "panel-32"}, "height": 8, "width": 8, "x": 16, "y": 20}},

          # Row 5: Logs (Loki Stream) [y=28, h=8]
          {"kind": "GridLayoutItem", "spec": {"element": {"kind": "ElementReference", "name": "panel-40"}, "height": 8, "width": 24, "x": 0, "y": 28}}
        ]
      }
    },
    "links": [],
    "liveNow": False,
    "preload": False,
    "tags": [
      "flac-analyzer",
      "orchestrator",
      "audio",
      "demucs",
      "victoriametrics",
      "loki"
    ],
    "timeSettings": {
      "autoRefresh": "10s",
      "autoRefreshIntervals": [
        "5s",
        "10s",
        "30s",
        "1m",
        "5m",
        "15m"
      ],
      "fiscalYearStartMonth": 0,
      "from": "now-3h",
      "hideTimepicker": False,
      "timezone": "Asia/Tokyo",
      "to": "now"
    },
    "title": "🎵 FLAC Analyzer - パイプライン監視 ＆ ボトルネック分析",
    "variables": [
      {
        "kind": "DatasourceVariable",
        "spec": {
          "allowCustomValue": True,
          "current": {
            "text": "victoriametrics-metrics-datasource",
            "value": "dfptek8zvsgzkd"
          },
          "hide": "inControlsMenu",
          "includeAll": False,
          "label": "db_victoriametrics",
          "multi": False,
          "name": "db_victoriametrics",
          "options": [],
          "pluginId": "victoriametrics-metrics-datasource",
          "refresh": "onDashboardLoad",
          "regex": "",
          "skipUrlSync": False
        }
      },
      {
        "kind": "DatasourceVariable",
        "spec": {
          "allowCustomValue": True,
          "current": {
            "text": "loki",
            "value": "efptf4f0yu0hsf"
          },
          "hide": "inControlsMenu",
          "includeAll": False,
          "label": "db_loki",
          "multi": False,
          "name": "db_loki",
          "options": [],
          "pluginId": "loki",
          "refresh": "onDashboardLoad",
          "regex": "",
          "skipUrlSync": False
        }
      }
    ]
  }
}

os.makedirs("dashboards", exist_ok=True)
out_path = os.path.join("dashboards", "flac_analyzer_dashboard.json")
with open(out_path, "w", encoding="utf-8") as f:
    json.dump(dashboard, f, ensure_ascii=False, indent=2)

print(f"Generated dashboard at {out_path} ({len(json.dumps(dashboard))} bytes)")
