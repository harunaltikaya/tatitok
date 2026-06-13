// Thin ECharts wrapper. ECharts measures its container ONCE at init: a
// container that layout has not sized yet yields a 0×0 canvas that
// never recovers on its own — the hard-stop-2 rendering bug (charts
// appeared only because opening devtools fired a window resize into the
// old resize listener). A ResizeObserver owns the lifecycle instead:
// init is deferred to the first nonzero measurement, every later size
// change resizes the chart, and window resizes are covered for free.

import { useEffect, useRef } from "react";
import * as echarts from "echarts";

// onSeriesClick (M5 Task 4): chart segments and legend entries are
// facet filters — a click on either reports the series name to the
// global filter state. Legend clicks are intercepted: the default
// hide-series toggle is undone (filtering changes the DATA, not the
// rendering), then reported like a segment click.
export default function Chart({
  option,
  height = "100%",
  onSeriesClick,
}: {
  option: echarts.EChartsOption;
  // height fills the panel body by default (M6 Task 4) so fullscreen and
  // resize grow the chart; the ResizeObserver below owns the redraw.
  height?: number | string;
  onSeriesClick?: (seriesName: string) => void;
}) {
  const el = useRef<HTMLDivElement>(null);
  const chart = useRef<echarts.ECharts | null>(null);
  const latest = useRef(option);
  latest.current = option;
  const clickRef = useRef(onSeriesClick);
  clickRef.current = onSeriesClick;

  useEffect(() => {
    const node = el.current;
    if (!node) return;
    const ro = new ResizeObserver(() => {
      if (node.clientWidth === 0 || node.clientHeight === 0) {
        return; // not laid out yet — init would bake in 0×0
      }
      if (!chart.current) {
        // SVG renderer + animation:false (per the design system) for crisp,
        // capturable output. The init is still deferred to the first nonzero
        // measurement; the click + legend-intercept handlers are unchanged.
        chart.current = echarts.init(node, null, { renderer: "svg" });
        chart.current.setOption(latest.current, { notMerge: true });
        chart.current.on("click", (params) => {
          const name = (params as { seriesName?: string }).seriesName;
          if (name) clickRef.current?.(name);
        });
        chart.current.on("legendselectchanged", (params) => {
          const name = (params as { name?: string }).name;
          if (!name) return;
          // Undo the visibility toggle, then filter instead.
          chart.current?.dispatchAction({ type: "legendSelect", name });
          clickRef.current?.(name);
        });
      } else {
        chart.current.resize();
      }
    });
    ro.observe(node);
    return () => {
      ro.disconnect();
      chart.current?.dispose();
      chart.current = null;
    };
  }, []);

  useEffect(() => {
    chart.current?.setOption(option, { notMerge: true });
  }, [option]);

  return <div ref={el} style={{ height }} />;
}
