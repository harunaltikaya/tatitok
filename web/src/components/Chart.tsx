// Thin ECharts wrapper. ECharts measures its container ONCE at init: a
// container that layout has not sized yet yields a 0×0 canvas that
// never recovers on its own — the hard-stop-2 rendering bug (charts
// appeared only because opening devtools fired a window resize into the
// old resize listener). A ResizeObserver owns the lifecycle instead:
// init is deferred to the first nonzero measurement, every later size
// change resizes the chart, and window resizes are covered for free.

import { useEffect, useRef } from "react";
import * as echarts from "echarts";

export default function Chart({ option, height = 280 }: { option: echarts.EChartsOption; height?: number }) {
  const el = useRef<HTMLDivElement>(null);
  const chart = useRef<echarts.ECharts | null>(null);
  const latest = useRef(option);
  latest.current = option;

  useEffect(() => {
    const node = el.current;
    if (!node) return;
    const ro = new ResizeObserver(() => {
      if (node.clientWidth === 0 || node.clientHeight === 0) {
        return; // not laid out yet — init would bake in 0×0
      }
      if (!chart.current) {
        chart.current = echarts.init(node);
        chart.current.setOption(latest.current, { notMerge: true });
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
