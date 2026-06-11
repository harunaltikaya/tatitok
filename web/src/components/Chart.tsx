// Thin ECharts wrapper: one chart instance per mount, options in, no
// external assets (echarts is bundled and embedded in the binary).

import { useEffect, useRef } from "react";
import * as echarts from "echarts";

export default function Chart({ option, height = 280 }: { option: echarts.EChartsOption; height?: number }) {
  const el = useRef<HTMLDivElement>(null);
  const chart = useRef<echarts.ECharts | null>(null);

  useEffect(() => {
    if (!el.current) return;
    chart.current = echarts.init(el.current);
    const onResize = () => chart.current?.resize();
    window.addEventListener("resize", onResize);
    return () => {
      window.removeEventListener("resize", onResize);
      chart.current?.dispose();
      chart.current = null;
    };
  }, []);

  useEffect(() => {
    chart.current?.setOption(option, { notMerge: true });
  }, [option]);

  return <div ref={el} style={{ height }} />;
}
