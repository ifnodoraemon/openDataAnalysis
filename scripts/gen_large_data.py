#!/usr/bin/env python3
"""Generate large realistic business datasets for testing."""
import csv
import random
import os
import math
from datetime import date, datetime, timedelta
from pathlib import Path

BASE = Path(__file__).resolve().parents[1] / "samples" / "coverage_scenarios"
random.seed(42)

# ── Helpers ──────────────────────────────────────────────────────
def write_csv(path, rows):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(rows[0])
        for r in rows[1:]:
            w.writerow(r)
    print(f"  {path}  ({len(rows)-1} 行)")

# ── 22: Daily retail transactions (~6000 rows) ───────────────────
def gen_daily_retail():
    regions = ["华东", "华南", "华北", "西南", "华中", "西北", "东北"]
    channels = ["天猫", "京东", "抖音", "拼多多", "线下门店", "小程序", "美团"]
    categories = ["手机数码", "家用电器", "服饰鞋包", "美妆个护", "食品生鲜", "家居家装", "母婴玩具", "运动户外"]
    products = [f"{cat}-SKU{i:03d}" for cat in categories for i in range(1, 9)]

    rows = [["date","region","channel","category","product_id","orders","units","gmv","discount","refund_amount","new_customers","returning_customers","avg_session_seconds","impressions","clicks","ctr"]]
    d = date(2024, 1, 1)
    for day_idx in range(731):
        d = date(2024, 1, 1) + timedelta(days=day_idx)
        ds = d.isoformat()
        n = 6 + random.randint(0, 6)
        for _ in range(n):
            r = random.choice(regions)
            ch = random.choice(channels)
            cat = random.choice(categories)
            prod = random.choice([p for p in products if p.startswith(cat)])

            base = 10 + random.uniform(0, 50) * (1 + math.sin(day_idx/30)*0.3)
            orders = max(1, int(random.gauss(base, base*0.3)))
            aov = random.gauss(120, 40)
            gmv = round(orders * aov, 2)
            disc = round(random.uniform(0, 0.25) * gmv, 2)
            refund = round(random.uniform(0, 0.08) * gmv, 2)
            new_cust = max(0, int(orders * random.uniform(0.1, 0.4)))
            ret_cust = orders - new_cust
            sess = random.randint(30, 600)
            imps = orders * random.randint(3, 30)
            ctr = round(random.uniform(0.01, 0.15), 4)
            clicks = max(orders, int(imps * ctr))

            rows.append([ds, r, ch, cat, prod, orders, max(1, int(orders*random.uniform(0.8,1.5))),
                         gmv, disc, refund, new_cust, ret_cust, sess, imps, clicks, round(clicks/imps, 4) if imps else 0])
    write_csv(BASE / "22_daily_retail_large" / "daily_retail_transactions.csv", rows)

# ── 23: Financial ledger (~4000 rows) ────────────────────────────
def gen_financial_ledger():
    departments = ["云平台事业部", "数据智能部", "AI研究院", "企业服务部", "技术支持部"]
    accounts = [
        # 收入类
        ("订阅服务收入","income"), ("一次性授权收入","income"), ("技术咨询收入","income"),
        ("培训服务收入","income"), ("运维服务收入","income"),
        # 成本类
        ("服务器成本","cogs"), ("带宽流量成本","cogs"), ("软件许可成本","cogs"),
        ("第三方服务费","cogs"), ("内容采购成本","cogs"),
        # 费用类
        ("工资薪酬","opex_salary"), ("社保公积金","opex_salary"), ("绩效奖金","opex_salary"),
        ("办公租金","opex_admin"), ("差旅交通","opex_admin"), ("招待费","opex_admin"),
        ("市场推广费","opex_mkt"), ("广告投放费","opex_mkt"), ("活动会展费","opex_mkt"),
        ("研发工具费","opex_rd"), ("专利注册费","opex_rd"), ("测试认证费","opex_rd"),
        ("折旧摊销","opex_da"), ("坏账准备","opex_other"), ("汇兑损益","opex_other"),
    ]
    cost_centers = [f"CC{dept[:2]}{i:02d}" for dept in departments for i in range(1, 6)]
    rows = [["date","department","cost_center","account_name","account_type","debit_amount","credit_amount","voucher_id","description","currency","exchange_rate"]]
    
    d = date(2024, 1, 1)
    vid = 100000
    for day_idx in range(731):
        d = date(2024, 1, 1) + timedelta(days=day_idx)
        if d.weekday() >= 5:
            if random.random() < 0.7:
                continue
        ds = d.isoformat()
        n = 3 + random.randint(0, 8)
        for _ in range(n):
            vid += 1
            dept = random.choice(departments)
            cc = random.choice(cost_centers)
            acct_name, acct_type = random.choice(accounts)
            if acct_type == "income":
                amt = random.gauss(50000, 25000)
            elif acct_type == "cogs":
                amt = random.gauss(30000, 15000)
            else:
                amt = random.gauss(8000, 6000)
            debit = round(max(0, amt + random.uniform(-amt*0.2, amt*0.2)), 2)
            credit = round(debit * random.uniform(0.95, 1.05), 2)
            curr = random.choices(["CNY","USD","EUR","JPY"], weights=[0.7, 0.15, 0.08, 0.07])[0]
            fx = 1.0 if curr == "CNY" else round(random.uniform(6.5, 7.8), 4)
            rows.append([ds, dept, cc, acct_name, acct_type, debit, credit,
                         f"V-{vid:06d}", f"{acct_name}-{ds}-{random.randint(1,99):02d}",
                         curr, fx])
    write_csv(BASE / "23_financial_ledger_large" / "general_ledger_daily.csv", rows)

# ── 24: Server metrics hourly (~8000 rows) ───────────────────────
def gen_server_metrics():
    clusters = [f"cluster-{x}" for x in ["alpha","beta","gamma","delta","epsilon"]]
    services = ["api-gateway","user-service","order-service","payment-service",
                "inventory-service","recommendation-engine","search-service",
                "notification-service","data-pipeline","ml-inference"]
    regions = ["us-east-1","us-west-2","eu-west-1","ap-southeast-1","ap-northeast-1"]

    rows = [["timestamp","cluster","region","service","instance_id","cpu_pct","memory_pct","disk_pct",
             "network_in_mbps","network_out_mbps","request_count","error_count","error_rate",
             "p50_latency_ms","p95_latency_ms","p99_latency_ms","active_connections",
             "queue_depth","gc_pause_ms","thread_count","uptime_seconds"]]

    # Generate hourly data for 3 months (~2200 hours) × a few services = ~8000 rows
    base_dt = datetime(2025, 7, 1, 0, 0, 0)
    instance_id = 1000
    for cluster in clusters:
        for svc in random.sample(services, 4):
            svc_regions = random.sample(regions, 3)
            for region_name in svc_regions:
                instance_id += 1
                inst = f"i-{instance_id:06d}"
                # Each instance gets a unique performance profile
                base_cpu = random.gauss(35, 15)
                base_mem = random.gauss(55, 10)
                base_latency = random.gauss(8, 3)
                base_err = random.uniform(0.0001, 0.005)
                base_conn = random.randint(50, 500)
                uptime = random.randint(100000, 2592000)

                for hour_idx in range(0, 2208, random.choice([1, 2, 3, 4, 6])):
                    dt = base_dt + timedelta(hours=hour_idx)
                    ts = dt.isoformat() + "Z"
                    hour = dt.hour
                    dow = dt.weekday()

                    # Add diurnal + weekly patterns
                    time_factor = 1.0 + 0.3 * (0.5 - abs(hour - 11) / 12.0)
                    weekend_factor = 0.6 if dow >= 5 else 1.0

                    cpu = min(100, max(0.1, base_cpu * time_factor * weekend_factor + random.gauss(0, 5)))
                    mem = min(100, max(1, base_mem * time_factor + random.gauss(0, 3)))
                    disk = min(95, random.gauss(42, 8))
                    net_in = max(0, random.gauss(80, 30) * time_factor * weekend_factor)
                    net_out = max(0, random.gauss(120, 40) * time_factor * weekend_factor)
                    reqs = max(1, int(random.gauss(500, 200) * time_factor * weekend_factor))
                    err_rate = min(1, base_err * (1 + random.uniform(-0.5, 1.0)))
                    errs = max(0, int(reqs * err_rate))
                    actual_err = round(errs/reqs, 6) if reqs else 0

                    p50 = max(0.5, base_latency * random.uniform(0.7, 1.5))
                    p95 = max(p50, p50 * random.uniform(2, 5))
                    p99 = max(p95, p95 * random.uniform(1.5, 3))
                    conns = int(base_conn * time_factor + random.gauss(0, 10))
                    queue = max(0, int(random.gauss(10, 8)))
                    gc = random.uniform(0, 5) if random.random() < 0.1 else 0
                    threads = random.randint(20, 200)
                    uptime += random.randint(1, 10) * 3600

                    rows.append([ts, cluster, region_name, svc, inst,
                                 round(cpu, 2), round(mem, 2), round(disk, 2),
                                 round(net_in, 2), round(net_out, 2), reqs, errs, actual_err,
                                 round(p50, 2), round(p95, 2), round(p99, 2),
                                 conns, queue, round(gc, 2), threads, uptime])
    write_csv(BASE / "24_server_metrics_large" / "server_metrics_hourly.csv", rows)

# ── 25: Wide employee table (~800 rows, 32 columns) ─────────────
def gen_employee_wide():
    departments = ["工程研发","产品设计","市场营销","销售业务","运营服务","人力行政","财务管理","法务合规","战略投资","客户成功"]
    titles = ["初级工程师","工程师","高级工程师","资深工程师","技术专家","产品经理","高级产品经理","产品总监",
              "市场专员","市场经理","市场总监","销售代表","销售经理","大区总监","运营专员","运营经理","运营总监",
              "HR专员","HR经理","财务专员","财务经理","法务专员","法务经理","战略分析师","战略总监","客户经理"]
    locations = ["北京","上海","深圳","杭州","成都","武汉","广州","南京","西安","新加坡"]
    education = ["专科","本科","硕士","博士","MBA"]
    bands = ["P4","P5","P6","P7","P8","P9","M3","M4","M5"]

    rows = [["employee_id","name","department","title","band","location","office","hire_date","tenure_years",
             "manager_id","direct_reports","base_salary","bonus_target_pct","equity_grant_value","total_comp",
             "last_performance_rating","last_promotion_date","education","major","certification_count",
             "remote_ratio","overtime_hours_yearly","training_hours","attendance_pct",
             "employee_satisfaction","flight_risk_score","critical_role","succession_ready",
             "key_skill_1","key_skill_2","key_skill_3","gender","age"]]

    managers = []
    for i in range(800):
        emp_id = f"EMP{i+1:04d}"
        dept = random.choice(departments)
        title = random.choice(titles)
        band = random.choice(bands)
        loc = random.choice(locations)
        office = f"{loc}{random.choice(['总部','研发中心','运营中心','分公司'])}"
        hire_date = date(2015, 1, 1) + timedelta(days=random.randint(0, 365*10))
        tenure = round((date(2025, 12, 31) - hire_date).days / 365.25, 1)
        
        mgr = random.choice(managers) if managers and random.random() < 0.7 else None
        mgr_id = mgr[0] if mgr else ""
        if not mgr and i < 80:
            managers.append([emp_id, dept])
            
        reports = random.randint(0, 12) if emp_id in [m[0] for m in managers] else 0
        
        base = round(random.gauss(20000, 12000) * (1 + tenure*0.08), 0)
        base = max(8000, min(120000, base))
        bonus_pct = random.uniform(0.05, 0.30)
        equity = round(base * random.uniform(0, 2) * (1 + tenure * 0.2), 0) if band.startswith("P") and int(band[1]) >= 6 else 0
        total = round(base * 12 * (1 + bonus_pct) + equity, 0)
        
        perf = random.choices(["S","A","B+","B","C"], weights=[0.1, 0.35, 0.3, 0.2, 0.05])[0]
        last_promo = date.today() - timedelta(days=random.randint(180, 1800))
        
        edu = random.choice(education)
        major = random.choice(["计算机科学","软件工程","电子工程","数学","统计学","工商管理","市场营销","金融学","经济学","人力资源管理","法学","设计学"])
        certs = random.randint(0, 5)
        remote = round(random.uniform(0, 1.0), 2)
        ot = random.randint(0, 400)
        training = random.randint(8, 120)
        attendance = round(random.gauss(0.95, 0.04), 2)
        satis = round(random.uniform(3.0, 5.0), 1)
        flight = round(random.uniform(0, 1.0), 2)
        critical = random.random() < 0.15
        succ = random.random() < 0.3 if critical else random.random() < 0.1
        gender = random.choices(["男","女"], weights=[0.55, 0.45])[0]
        age = int(random.gauss(32 + tenure * 1.2, 6))
        age = max(22, min(60, age))

        skills = random.sample([
            "Python","Go","Java","TypeScript","React","Vue","Kubernetes","Docker","AWS","GCP","Azure",
            "Spark","Flink","Kafka","PostgreSQL","MySQL","Redis","Elasticsearch","MongoDB","GraphQL",
            "Machine Learning","Deep Learning","NLP","Computer Vision","Data Engineering",
            "Product Strategy","User Research","A/B Testing","Growth Hacking",
            "SEO/SEM","Content Marketing","Social Media","Brand Management",
            "Negotiation","Key Account Management","Pipeline Management",
            "Financial Modeling","Budget Planning","Audit","Tax",
            "Contract Review","IP Management","Compliance",
            "Agile/Scrum","OKR Planning","Change Management",
            "Statistical Analysis","Data Visualization","SQL","Excel","Tableau",
        ], 3)

        rows.append([emp_id, f"{random.choice('张王李赵刘陈杨黄周吴徐孙马朱胡郭何高林郑')}{random.choice('伟芳秀英敏静丽强磊军洋勇艳娟涛明超平刚华建文')}",
                     dept, title, band, loc, office, hire_date.isoformat(), tenure,
                     mgr_id, reports, base, round(bonus_pct, 2), equity, total,
                     perf, last_promo.isoformat(), edu, major, certs,
                     remote, ot, training, attendance,
                     satis, flight, critical, succ,
                     skills[0], skills[1], skills[2], gender, age])
    write_csv(BASE / "25_employee_wide" / "employee_master.csv", rows)

# ── Run ───────────────────────────────────────────────────────────
if __name__ == "__main__":
    print("正在生成大规模测试数据集...")
    gen_daily_retail()
    gen_financial_ledger()
    gen_server_metrics()
    gen_employee_wide()
    print("完成。")
