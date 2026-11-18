import type { WebLink } from "./models";

const SELF_HOSTED = "Self-hosted";
const EMBEDDED = "Embedded";
const ROBOT = "Robot";
const UAV = "UAV";
const HUMANOID = "Humanoid robot";
const LEARN_AI = "Learn AI";
const BOOKS = "Books";
// v2 also files the user's own copies of a default (same URL, or same name
// such as "jenkins" for "Jenkins (builds)"), which v1 left uncategorized.
const CATEGORIES_MARKER = "categories:v2";

/** The default a saved link stands for: same id, same URL, or same leading name word. */
function presetFor(link: WebLink): WebLink | undefined {
  const name = link.name.trim().toLowerCase();
  return DEFAULT_WEB_LINKS.find((preset) => preset.id === link.id || preset.url === link.url
    || preset.name.toLowerCase().split(/[\s(]/)[0] === name);
}

/**
 * Sites offered in every workspace's Web tab. Each one is added once per
 * workspace (tracked by id in the persisted `seeded` list), so a link the user
 * deletes or edits stays that way, and a link added here later still shows up.
 * Ids are stable: change a URL or name here, never the id.
 */
export const DEFAULT_WEB_LINKS: readonly WebLink[] = [
  { id: "default:zotero-webdav", name: "Zotero WebDAV", url: "http://9.134.118.150:18100/", category: SELF_HOSTED },
  { id: "default:multica-server", name: "Multica server (sync)", url: "http://9.134.118.150:18080/", category: SELF_HOSTED },
  { id: "default:miniflux", name: "Miniflux (RSS)", url: "http://9.134.118.150:18090/", category: SELF_HOSTED },
  { id: "default:syncthing", name: "Syncthing (file sync)", url: "http://9.134.118.150:8384/", category: SELF_HOSTED },
  { id: "default:vaultwarden", name: "Vaultwarden (passwords)", url: "https://9.134.118.150:8222/", category: SELF_HOSTED },
  { id: "default:memos", name: "Memos", url: "http://9.134.118.150:5230/", category: SELF_HOSTED },
  { id: "default:jenkins", name: "Jenkins (builds)", url: "http://9.134.118.150:8080/", category: SELF_HOSTED },
  { id: "default:uptime-kuma", name: "Uptime Kuma (monitor)", url: "http://9.134.118.150:3001/", category: SELF_HOSTED },
  { id: "default:ntfy", name: "ntfy (notifications)", url: "http://9.134.118.150:2586/", category: SELF_HOSTED },

  { id: "default:nanocode-youlan", name: "Nanocode Youlan docs", url: "https://www.nanocode.cn/wiki/docs/youlan", category: EMBEDDED },
  { id: "default:orangepi", name: "Orange Pi", url: "http://www.orangepi.cn/", category: EMBEDDED },
  { id: "default:radxa", name: "Radxa", url: "https://www.radxa.cn/", category: EMBEDDED },
  { id: "default:waveshare", name: "Waveshare", url: "https://www.waveshare.net/", category: EMBEDDED },
  { id: "default:xinbanfang", name: "Xinbanfang", url: "https://www.xinbanfang.com/", category: EMBEDDED },
  { id: "default:youyeetoo", name: "Youyeetoo", url: "https://youyeetoo.com/", category: EMBEDDED },
  { id: "default:lckfb", name: "LCKFB (立创开发板)", url: "https://lckfb.com/", category: EMBEDDED },
  { id: "default:alientek", name: "ALIENTEK (正点原子)", url: "https://www.alientek.com/", category: EMBEDDED },
  { id: "default:openluat", name: "OpenLuat (合宙)", url: "https://www.openluat.com/", category: EMBEDDED },
  { id: "default:sipeed", name: "Sipeed", url: "https://sipeed.com/", category: EMBEDDED },
  { id: "default:milkv", name: "Milk-V", url: "https://milkv.io/zh/", category: EMBEDDED },
  { id: "default:luckfox", name: "Luckfox", url: "https://www.luckfox.com/", category: EMBEDDED },
  { id: "default:100ask", name: "100ASK (百问网) hardware", url: "https://www.100ask.net/hardware/", category: EMBEDDED },
  { id: "default:embedfire", name: "EmbedFire (野火)", url: "https://embedfire.com/", category: EMBEDDED },

  { id: "default:yahboom", name: "Yahboom (亚博智能)", url: "https://www.yahboom.com/", category: ROBOT },
  { id: "default:hiwonder", name: "Hiwonder (幻尔)", url: "https://www.hiwonder.com/", category: ROBOT },
  { id: "default:wheeltec", name: "WHEELTEC (轮趣)", url: "https://wheeltec.net/", category: ROBOT },
  { id: "default:dfrobot", name: "DFRobot", url: "https://www.dfrobot.com.cn/", category: ROBOT },
  { id: "default:slamtec", name: "SLAMTEC (思岚)", url: "https://www.slamtec.com/", category: ROBOT },
  { id: "default:orbbec", name: "Orbbec (奥比中光)", url: "https://www.orbbec.com.cn/", category: ROBOT },
  { id: "default:jlcfa", name: "JLCFA", url: "https://www.jlcfa.com/", category: ROBOT },


  // Flight controllers, open-source drones and dev kits
  { id: "default:micoair", name: "MicoAir (微空科技) flight controllers", url: "https://micoair.cn/", category: UAV },
  { id: "default:anotc", name: "ANO (匿名科创) flight controllers", url: "https://www.anotc.com/", category: UAV },
  { id: "default:holybro", name: "Holybro (Pixhawk, PX4 kits)", url: "https://holybro.com/", category: UAV },
  { id: "default:cuav", name: "CUAV (雷迅创新) autopilots", url: "https://www.cuav.net/", category: UAV },
  { id: "default:amovlab", name: "AMOVLAB (阿木实验室) / Prometheus", url: "https://www.amovlab.com/", category: UAV },
  { id: "default:speedybee", name: "SpeedyBee (FPV)", url: "https://www.speedybee.com/", category: UAV },
  { id: "default:mateksys", name: "MATEKSYS flight controllers", url: "https://www.mateksys.com/", category: UAV },
  // Motors, ESCs, video/data links, batteries
  { id: "default:tmotor", name: "T-MOTOR motors & ESCs", url: "https://uav-cn.tmotor.com/", category: UAV },
  { id: "default:hobbywing", name: "HOBBYWING (好盈) ESCs", url: "https://www.hobbywing.com/", category: UAV },
  { id: "default:siyi", name: "SIYI (思翼科技) links & gimbals", url: "https://siyi.biz/", category: UAV },
  { id: "default:skydroid", name: "SKYDROID (云卓科技) controllers & links", url: "https://www.skydroid.xin/", category: UAV },
  { id: "default:grepow", name: "GREPOW / Tattu (格瑞普) batteries", url: "https://www.grepow.cn/", category: UAV },

  { id: "default:hightorque", name: "HighTorque (高擎机电)", url: "https://www.hightorque.cn/", category: HUMANOID },
  { id: "default:unitree", name: "Unitree (宇树)", url: "https://www.unitree.com/", category: HUMANOID },
  { id: "default:roboparty", name: "RoboParty", url: "https://roboparty.com/", category: HUMANOID },
  { id: "default:engineai", name: "EngineAI (众擎) products", url: "https://en.engineai.com.cn/product-purchase.html", category: HUMANOID },
  { id: "default:booster-k1", name: "Booster Robotics K1 (加速进化)", url: "https://www.booster.tech/cn/booster-k1/", category: HUMANOID },
  // Desktop arms for embodied-AI work (grasping, teleoperation, demonstration data, imitation learning)
  { id: "default:robstride-a3", name: "RobStride EduLite A3 open-source arm (GitHub)", url: "https://github.com/RobStride/EDULITE_A3", category: HUMANOID },
  { id: "default:waveshare-so-arm", name: "Waveshare SO-ARM100/101 kit (LeRobot)", url: "https://www.waveshare.net/shop/SO-ARM100-3DP-Parts-Kit.htm", category: HUMANOID },
  { id: "default:waveshare-so-arm-wiki", name: "Waveshare SO-ARM100/101 wiki", url: "https://www.waveshare.net/wiki/SO-ARM100/101", category: HUMANOID },
  { id: "default:agilex-piper", name: "AgileX PiPER arm (松灵)", url: "https://www.agilex.ai/product/piper", category: HUMANOID },
  // Joint motors and servos for building your own robot
  { id: "default:robstride", name: "RobStride (灵足时代) joint motors", url: "https://www.robstride.com/", category: HUMANOID },
  { id: "default:dmbot", name: "DM Robot (达妙) joint motors", url: "https://dmbot.cn/", category: HUMANOID },
  { id: "default:feetech", name: "FEETECH (飞特) bus servos", url: "https://www.feetech.cn/", category: HUMANOID },

  { id: "default:learnpytorch", name: "Learn PyTorch for Deep Learning (Zero to Mastery)", url: "https://www.learnpytorch.io/", category: LEARN_AI },
  { id: "default:learnhuggingface", name: "Learn Hugging Face", url: "https://www.learnhuggingface.com/", category: LEARN_AI },
  { id: "default:d2l", name: "Dive into Deep Learning (d2l.ai)", url: "https://d2l.ai/", category: LEARN_AI },

  { id: "default:oceanofpdf", name: "OceanofPDF", url: "https://oceanofpdf.com/", category: BOOKS },
  { id: "default:weread", name: "WeRead (微信读书)", url: "https://weread.qq.com/", category: BOOKS },
];

/**
 * Defaults whose category changed after workspaces were seeded. Applied once
 * per workspace, and only while the link still has the old category, so a
 * category the user picked is never overwritten.
 */
const CATEGORY_MOVES: readonly { id: string; from: string; to: string; marker: string }[] = [
  { id: "default:dmbot", from: ROBOT, to: HUMANOID, marker: "move:dmbot-humanoid" },
  { id: "default:feetech", from: ROBOT, to: HUMANOID, marker: "move:feetech-humanoid" },
];

/**
 * Append the defaults this workspace hasn't been given yet (skipping URLs it
 * already has), and give already-seeded defaults their category if they were
 * saved before categories existed.
 */
export function seedDefaultLinks(links: WebLink[], seeded: readonly string[]): { links: WebLink[]; seeded: string[] } {
  // One-time backfill, so a category the user clears later stays cleared.
  const backfill = !seeded.includes(CATEGORIES_MARKER);
  const withCategories = links.map((link) => {
    const move = CATEGORY_MOVES.find((m) => m.id === link.id && !seeded.includes(m.marker));
    if (move && link.category === move.from) return { ...link, category: move.to };
    if (!backfill || link.category) return link;
    const category = presetFor(link)?.category;
    return category ? { ...link, category } : link;
  });
  const done = new Set(seeded);
  const urls = new Set(links.map((link) => link.url));
  const ids = new Set(links.map((link) => link.id));
  const added = DEFAULT_WEB_LINKS.filter((link) => !done.has(link.id) && !ids.has(link.id) && !urls.has(link.url));
  return {
    links: [...withCategories, ...added.map((link) => ({ ...link }))],
    seeded: [...new Set([...seeded, ...DEFAULT_WEB_LINKS.map((link) => link.id), CATEGORIES_MARKER, ...CATEGORY_MOVES.map((m) => m.marker)])],
  };
}
