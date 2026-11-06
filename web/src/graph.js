import ForceGraph3D from "3d-force-graph";
import ForceGraph from "force-graph";
import { forceCollide } from "d3-force-3d";

const colors = {
  entities: "#62c983",
  memories: "#e9aa44",
  topics: "#ffffff",
  stories: "#ed7474",
};

// Labels are DOM text: record names and relationship types may contain markup.
function label(text) {
  const element = document.createElement("span");
  element.textContent = text;
  return element;
}

const nodeRadius = (node) => 4 * Math.sqrt(1 + node.connections);

export function memoryGraph(container, inspect, mode = "2d") {
  const flat = mode === "2d";
  let fit = true,
    matches = null;
  const graph = new (flat ? ForceGraph : ForceGraph3D)(container)
    .backgroundColor("#000000")
    .nodeColor((node) =>
      matches && !matches.has(node.id)
        ? "#283039"
        : colors[node.kind] || "#ffffff",
    )
    .nodeVal((node) => 1 + node.connections)
    .nodeLabel((node) => label(node.title || node.id))
    .linkColor((link) =>
      !matches ||
      matches.has(link.source.id || link.source) ||
      matches.has(link.target.id || link.target)
        ? flat
          ? "#94a3b859"
          : "#94a3b8"
        : "#182028",
    )
    .linkLabel((link) => label(link.type))
    .linkDirectionalArrowLength(2)
    .onNodeClick((node) => inspect(node.id))
    .cooldownTicks(120)
    .onEngineStop(() => {
      if (fit && graph.graphData().nodes.length) {
        graph.zoomToFit(400, 36);
        fit = false;
      }
    });
  if (flat) {
    graph
      .warmupTicks(80)
      .d3Force(
        "collide",
        forceCollide((node) => nodeRadius(node) + 4).iterations(3),
      );
    graph
      .d3Force("link")
      .distance(
        (link) => nodeRadius(link.source) + nodeRadius(link.target) + 28,
      );
  } else graph.showNavInfo(false).nodeOpacity(1).linkOpacity(0.35);
  const resize = new ResizeObserver(() => {
    graph.width(container.clientWidth).height(container.clientHeight);
    if (!fit) graph.zoomToFit(0, 36);
  });
  resize.observe(container);
  const visibility = new IntersectionObserver(([entry]) => {
    if (entry.isIntersecting) graph.resumeAnimation();
    else graph.pauseAnimation();
  });
  visibility.observe(container);
  return {
    highlight(ids) {
      matches = ids === null ? null : new Set(ids);
      graph.nodeColor(graph.nodeColor()).linkColor(graph.linkColor());
    },
    focus(id) {
      if (!id) {
        graph.zoomToFit(500, 36);
        return;
      }
      const node = graph.graphData().nodes.find((node) => node.id === id);
      if (!node) return;
      fit = false;
      const { x = 0, y = 0, z = 0 } = node;
      if (flat) {
        const radius = nodeRadius(node);
        const zoom = Math.min(
          3,
          Math.min(container.clientWidth, container.clientHeight) /
            (radius * 6),
        );
        graph.centerAt(x, y, 650).zoom(zoom, 650);
      } else {
        const distance = 100 + Math.cbrt(1 + node.connections) * 12;
        graph.cameraPosition({ x, y, z: z + distance }, { x, y, z }, 650);
      }
    },
    destroy() {
      resize.disconnect();
      visibility.disconnect();
      const renderer = flat ? null : graph.renderer();
      graph._destructor();
      renderer?.forceContextLoss();
    },
    update(data) {
      // Both renderers mutate data. Give each its own nodes and links.
      const nodes = data.nodes.map((node) => ({ ...node, connections: 0 }));
      const byID = new Map(nodes.map((node) => [node.id, node]));
      const links = data.links
        .filter((link) => byID.has(link.source) && byID.has(link.target))
        .map((link) => ({ ...link }));
      for (const link of links) {
        byID.get(link.source).connections++;
        byID.get(link.target).connections++;
      }
      graph.graphData({ nodes, links });
    },
  };
}
