/* tatitok dashboard UI kit — ECharts option builders (window.TTCharts).
   Calm, flat charts: transparent backgrounds, gray axes, class-colored
   series, no decorative gradients. */
(function () {
  const { CLASS, CHART, days, classKeys, heat, models } = window.TT;
  const axisText = { color: CHART.axis, fontSize: 11, fontFamily: "Jost, sans-serif" };
  const tooltip = {
    backgroundColor: "#161618",
    borderColor: "rgba(255,255,255,0.09)",
    borderWidth: 0.5,
    textStyle: { color: "#e4e4e7", fontSize: 12, fontFamily: "Jost, sans-serif" },
    padding: [8, 11],
  };

  // Donut — API-equivalent value by economic class.
  function donutByClass() {
    const sums = {};
    for (const c of classKeys) sums[c] = 0;
    for (const d of days) for (const c of classKeys) sums[c] += d.byClass[c].equiv;
    const data = classKeys
      .filter((c) => sums[c] > 0)
      .map((c) => ({ name: CLASS[c].label, value: Math.round(sums[c]), itemStyle: { color: CLASS[c].color } }));
    return {
      backgroundColor: "transparent",
      animation: false,
      tooltip: { ...tooltip, trigger: "item", valueFormatter: (v) => "$" + v.toLocaleString() },
      series: [{
        type: "pie", radius: ["62%", "86%"], center: ["50%", "50%"],
        avoidLabelOverlap: false, padAngle: 2,
        itemStyle: { borderRadius: 4, borderColor: "#161618", borderWidth: 2 },
        label: { show: false }, labelLine: { show: false },
        emphasis: { scale: true, scaleSize: 4 },
        data,
      }],
    };
  }

  // Stacked daily bars by class. metric: 'tokens' | 'equiv' | 'actual'.
  function dailyStacked(metric) {
    const dates = days.map((d) => d.date.slice(5));
    const fmt = metric === "tokens"
      ? (v) => window.TT.compactTokens(v)
      : (v) => "$" + (v >= 1000 ? (v / 1000).toFixed(1) + "k" : v.toFixed(0));
    return {
      backgroundColor: "transparent",
      animation: false,
      grid: { left: 46, right: 10, top: 14, bottom: 22 },
      tooltip: {
        ...tooltip, trigger: "axis", axisPointer: { type: "shadow", shadowStyle: { color: "rgba(255,255,255,0.04)" } },
        valueFormatter: (v) => fmt(v),
      },
      xAxis: {
        type: "category", data: dates,
        axisLabel: { ...axisText, interval: 4 },
        axisLine: { lineStyle: { color: CHART.split } }, axisTick: { show: false },
      },
      yAxis: {
        type: "value", axisLabel: { ...axisText, formatter: (v) => fmt(v) },
        splitLine: { lineStyle: { color: CHART.split } },
      },
      series: classKeys.map((c) => ({
        name: CLASS[c].label, type: "bar", stack: "t",
        itemStyle: { color: CLASS[c].color },
        emphasis: { focus: "series" },
        barWidth: "62%",
        data: days.map((d) => Math.round(metric === "tokens" ? d.byClass[c].tokens : d.byClass[c][metric])),
      })),
    };
  }

  // Hour × weekday heatmap — when you work.
  function activityHeatmap() {
    const dows = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
    const data = heat.map(([h, w, v]) => [h, (w + 6) % 7, v]); // shift so Mon=0
    const max = Math.max(...heat.map((x) => x[2]));
    return {
      backgroundColor: "transparent",
      animation: false,
      tooltip: { ...tooltip, formatter: (p) => `${dows[p.value[1]]} ${String(p.value[0]).padStart(2, "0")}:00 · <b>${p.value[2].toFixed(2)}</b>` },
      grid: { left: 38, right: 12, top: 10, bottom: 26 },
      xAxis: {
        type: "category", data: Array.from({ length: 24 }, (_, i) => i),
        axisLabel: { ...axisText, interval: 2, formatter: (v) => v + "h" },
        axisLine: { show: false }, axisTick: { show: false }, splitArea: { show: false },
      },
      yAxis: {
        type: "category", data: dows,
        axisLabel: axisText, axisLine: { show: false }, axisTick: { show: false },
      },
      visualMap: {
        min: 0, max, show: false,
        inRange: { color: ["#161618", "#173a30", "#1c9d74", "#34d399"] },
      },
      series: [{
        type: "heatmap", data,
        itemStyle: { borderColor: "#09090b", borderWidth: 2, borderRadius: 3 },
        emphasis: { itemStyle: { borderColor: "#52525b" } },
      }],
    };
  }

  // Treemap — API-equivalent value by model, colored by class.
  function valueTreemap() {
    const data = models.map((m) => ({
      name: m.key, value: Math.round(m.equiv),
      itemStyle: { color: CLASS[m.cls].color },
    }));
    return {
      backgroundColor: "transparent",
      animation: false,
      tooltip: { ...tooltip, formatter: (p) => `${p.name}<br/><b>$${p.value.toLocaleString()}</b> API-equiv` },
      series: [{
        type: "treemap", roam: false, nodeClick: false, breadcrumb: { show: false },
        width: "100%", height: "100%", top: 0, left: 0, right: 0, bottom: 0,
        itemStyle: { borderColor: "#09090b", borderWidth: 3, gapWidth: 3, borderRadius: 4 },
        label: {
          show: true, fontFamily: "Jost, sans-serif", fontSize: 13, color: "#09090b",
          formatter: (p) => `{n|${p.name}}\n{v|$${p.value.toLocaleString()}}`,
          rich: {
            n: { fontSize: 13, fontWeight: 500, color: "rgba(9,9,11,0.9)", lineHeight: 18 },
            v: { fontSize: 12, color: "rgba(9,9,11,0.66)", lineHeight: 16, fontFeatureSettings: "tnum" },
          },
        },
        data,
      }],
    };
  }

  window.TTCharts = { donutByClass, dailyStacked, activityHeatmap, valueTreemap };
})();
