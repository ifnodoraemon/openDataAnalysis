#!/usr/bin/env python3
"""Generate cross-tabulation / pivot-friendly test datasets."""
import csv
import random
import os
from datetime import date, timedelta
from pathlib import Path

BASE = Path(__file__).resolve().parents[1] / "samples" / "coverage_scenarios"
random.seed(42)

def write_csv(path, rows):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", newline="") as f:
        w = csv.writer(f)
        for r in rows:
            w.writerow(r)
    print(f"  {path}  ({len(rows)-1} 行)")

# ── 26: Sales pivot matrix ───────────────────────────────────────
# product_category × region × month → revenue, units, margin
def gen_sales_pivot():
    categories = ["智能家电","消费电子","家居日用","服装鞋帽","美妆护肤","食品饮料","母婴用品","运动户外","图书文娱","汽车用品"]
    regions = ["华东","华南","华北","西南","华中","西北","东北","港澳台","海外亚太","海外欧美"]
    months = []
    for y in [2023, 2024, 2025]:
        for m in range(1, 13):
            d = date(y, m, 1)
            if d <= date(2025, 6, 1):
                months.append(d.isoformat()[:7])

    rows = [["month","region","product_category","category_rank","revenue","units_sold","gross_margin_pct","marketing_spend","discount_amount","return_amount","new_product_flag"]]
    for mo in months:
        for cat_idx, cat in enumerate(categories):
            for reg_idx, reg in enumerate(regions):
                base_rev = [420000,380000,350000,280000,320000,290000,250000,220000,180000,150000][cat_idx]
                base_rev *= [1.0,0.9,0.85,0.75,0.7,0.65,0.6,0.5,0.4,0.35][reg_idx]
                base_rev *= 0.85 + 0.15 * random.random()
                
                seasonal = [1.0,0.85,0.9,1.0,1.05,1.1,1.15,1.1,1.2,1.25,1.3,1.35][date(int(mo[:4]), int(mo[5:]), 1).month - 1]
                revenue = round(base_rev * seasonal * (0.9 + 0.2 * random.random()), 0)
                units = max(1, int(revenue / [800,600,200,120,90,50,150,220,40,300][cat_idx] * random.uniform(0.85, 1.15)))
                margin = round(random.uniform(0.15, 0.55) + [0.28,0.25,0.35,0.40,0.50,0.30,0.32,0.38,0.45,0.22][cat_idx] * 0.4, 3)
                mkt = round(revenue * random.uniform(0.05, 0.18), 0)
                disc = round(revenue * random.uniform(0.02, 0.12), 0)
                ret = round(revenue * random.uniform(0.01, 0.06), 0)
                new_prod = 1 if random.random() < 0.08 else 0

                rows.append([mo, reg, cat, cat_idx + 1, revenue, units, margin, mkt, disc, ret, new_prod])
    write_csv(BASE / "26_sales_pivot_matrix" / "sales_product_region_monthly.csv", rows)

# ── 27: Multi-level product hierarchy ────────────────────────────
def gen_product_hierarchy():
    hierarchy = {
        "家用电器": {
            "大家电": ["冰箱","洗衣机","空调","热水器","油烟机"],
            "小家电": ["电饭煲","微波炉","吸尘器","电风扇","净水器"],
            "个护电器": ["电动牙刷","剃须刀","吹风机","美容仪","按摩仪"],
        },
        "数码产品": {
            "手机通讯": ["智能手机","功能手机","固定电话","对讲机","卫星电话"],
            "电脑办公": ["笔记本","台式机","平板","显示器","打印机"],
            "智能穿戴": ["智能手表","手环","VR眼镜","蓝牙耳机","智能戒指"],
        },
        "食品饮料": {
            "休闲零食": ["坚果炒货","膨化食品","糖果巧克力","肉干肉脯","海苔"],
            "酒水饮品": ["白酒","啤酒","葡萄酒","碳酸饮料","茶饮料"],
            "生鲜食品": ["水果","蔬菜","肉类","海鲜","蛋奶"],
        },
    }
    rows = [["sku_id","sku_name","category_l1","category_l2","category_l3","brand","supplier","unit_price","cost_price","weight_kg","shelf_life_days","min_order_qty","stock_quantity","reorder_point","lead_time_days","abc_class","is_active"]]
    brands = ["美的","格力","海尔","小米","华为","苹果","三星","索尼","蒙牛","伊利","三只松鼠","良品铺子","元气森林","农夫山泉","戴森","飞利浦","联想","华硕","罗技","大疆"]
    suppliers = ["华东仓配中心","华南供应链","华北分销商","西南仓储","华中物流园","海外直采","品牌直销","跨境保税仓","产地直供","总代理"]
    
    sku_id = 10000
    for l1, l2_dict in hierarchy.items():
        for l2, l3_list in l2_dict.items():
            for l3 in l3_list:
                for color_variant in ["标准版","升级版","旗舰版"]:
                    sku_id += 1
                    brand = random.choice(brands)
                    supplier = random.choice(suppliers)
                    price = round(random.uniform(29.9, 8999), 2)
                    cost = round(price * random.uniform(0.35, 0.75), 2)
                    weight = round(random.uniform(0.1, 45.0), 2)
                    shelf = random.choice([90, 180, 270, 365, 540, 0])
                    min_q = random.choice([1, 2, 5, 10])
                    stock = random.randint(0, 5000)
                    reorder = max(50, int(stock * 0.2))
                    lead = random.randint(1, 45)
                    abc = random.choices(["A","B","C"], weights=[0.15, 0.35, 0.50])[0]
                    active = random.random() > 0.05
                    
                    rows.append([f"SKU{sku_id:06d}", f"{l3}-{color_variant}", l1, l2, l3,
                                 brand, supplier, price, cost, weight, shelf, min_q,
                                 stock, reorder, lead, abc, int(active)])
    write_csv(BASE / "27_product_hierarchy" / "product_master_hierarchy.csv", rows)

# ── 28: HR cross-tab (dept × location × month) ──────────────────
def gen_hr_crosstab():
    departments = ["研发中心","产品设计","销售业务","运营服务","市场营销","客户成功","人力行政","财务管理"]
    locations = ["北京","上海","深圳","杭州","成都","武汉","广州","南京","西安","新加坡","东京","伦敦"]
    months = []
    for y in [2023, 2024, 2025]:
        for m in range(1, 13):
            d = date(y, m, 1)
            if d <= date(2025, 6, 1):
                months.append(d.isoformat()[:7])

    rows = [["month","department","location","headcount","avg_salary","avg_tenure_years","attrition_count","new_hires","promotion_count","training_days","overtime_hours","revenue_per_employee","cost_per_employee","engagement_score","span_of_control"]]
    for mo in months:
        y, m = int(mo[:4]), int(mo[5:])
        for dept in departments:
            for loc in locations:
                base_hc = {"研发中心":120,"产品设计":45,"销售业务":80,"运营服务":55,"市场营销":40,"客户成功":35,"人力行政":22,"财务管理":18}[dept]
                loc_factor = {"北京":1.0,"上海":0.95,"深圳":0.85,"杭州":0.7,"成都":0.5,"武汉":0.4,"广州":0.55,"南京":0.35,"西安":0.3,"新加坡":0.2,"东京":0.15,"伦敦":0.12}[loc]
                hc = max(1, int(base_hc * loc_factor * (0.9 + 0.2 * (y - 2022) * 0.1)))
                hc += random.randint(-3, 5)
                if hc < 2:
                    continue

                avg_sal = round(random.uniform(8000, 45000) * loc_factor, 0)
                tenure = round(random.uniform(1.2, 8.5), 1)
                attr = max(0, int(hc * random.uniform(0.01, 0.08)))
                hires = max(0, attr + random.randint(-2, 5))
                promos = max(0, int(hc * random.uniform(0.02, 0.12)))
                training = random.randint(0, 40)
                ot = random.randint(0, int(hc * 8))
                rev_emp = round(avg_sal * 12 * random.uniform(1.5, 8.0), 0)
                cost_emp = round(avg_sal * 14 * random.uniform(1.1, 1.6), 0)
                engage = round(random.uniform(2.5, 5.0), 1)
                span = round(hc / max(1, random.randint(1, max(2, int(hc * 0.3)))), 1)

                rows.append([mo, dept, loc, hc, avg_sal, tenure, attr, hires, promos, training, ot, rev_emp, cost_emp, engage, span])
    write_csv(BASE / "28_hr_crosstab" / "hr_dept_location_monthly.csv", rows)

# ── 29: Cohort retention matrix ─────────────────────────────────
def gen_cohort_matrix():
    """Generate classic cohort retention data: rows=cohort_month, cols=period."""
    rows = [["cohort_month","period","initial_users","retained_users","retention_rate","arpu","total_revenue","active_days_avg","session_count_avg","feature_adopted_pct"]]
    
    for cohort_m in range(1, 19):
        cohort = date(2024, 1, 1) + timedelta(days=(cohort_m - 1) * 30)
        cohort_str = cohort.replace(day=1).isoformat()[:7]
        initial = random.randint(800, 5000)
        
        base_retention = 1.0
        for period in range(0, 18):
            if period == 0:
                rate = 1.0
            elif period == 1:
                rate = round(random.uniform(0.30, 0.55), 4)
                base_retention = rate
            else:
                decay = 0.85 + random.uniform(-0.05, 0.05)
                rate = round(base_retention * (decay ** (period - 1)), 4)
            
            if rate < 0.01 and period > 6:
                continue
            
            retained = max(0, int(initial * rate))
            arpu = round(random.uniform(15, 120) * (1 + period * 0.05), 2)
            rev = round(retained * arpu, 2)
            days = round(random.uniform(5, 28) * rate, 1)
            sessions = round(random.uniform(8, 60) * rate, 1)
            feature = round(rate * random.uniform(0.4, 0.9), 4)

            rows.append([cohort_str, period, initial, retained, rate, arpu, rev, days, sessions, feature])
    write_csv(BASE / "29_cohort_retention_matrix" / "user_cohort_retention.csv", rows)

# ── 30: Time series with dimensions ─────────────────────────────
def gen_timeseries_multidim():
    """Daily KPIs with multiple slicing dimensions for trend analysis."""
    platforms = ["iOS","Android","Web","小程序","快应用"]
    channels = ["自然增长","付费搜索","信息流广告","社交分享","KOL推荐","EDM","线下地推","SEO","异业合作","短信复购"]
    user_segments = ["新用户","活跃用户","沉默召回","流失预警","高价值VIP"]
    months = []
    for m in range(1, 13):
        months.append(f"2025-{m:02d}")

    rows = [["date","platform","channel","user_segment","dau","new_users","session_count","avg_session_duration","page_views","bounce_rate","conversion_rate","gmv","orders","aov","retention_d1","retention_d7"]]
    d = date(2025, 1, 1)
    while d <= date(2025, 12, 31):
        ds = d.isoformat()
        dow = d.weekday()
        for pl in random.sample(platforms, random.randint(2, 5)):
            for ch in random.sample(channels, random.randint(2, 6)):
                for seg in random.sample(user_segments, random.randint(1, 3)):
                    wday_factor = 0.7 if dow >= 5 else 1.0
                    base_dau = {"新用户":300,"活跃用户":2500,"沉默召回":150,"流失预警":80,"高价值VIP":400}[seg]
                    dau = max(1, int(base_dau * wday_factor * random.uniform(0.7, 1.3)))
                    new_u = max(0, int(dau * random.uniform(0.05, 0.3)))
                    sess = max(1, int(dau * random.uniform(1.5, 4.0)))
                    dur = round(random.uniform(30, 900), 1)
                    pv = max(1, int(sess * random.uniform(2, 12)))
                    bounce = round(random.uniform(0.15, 0.65), 3)
                    conv = round(random.uniform(0.005, 0.08), 4)
                    aov = round(random.uniform(30, 500), 2)
                    orders = max(0, int(dau * conv))
                    gmv = round(orders * aov, 2)
                    d1 = round(random.uniform(0.1, 0.5), 3)
                    d7 = round(random.uniform(0.05, 0.25), 3)

                    rows.append([ds, pl, ch, seg, dau, new_u, sess, dur, pv, bounce, conv, gmv, orders, aov, d1, d7])
        d += timedelta(days=1)
    write_csv(BASE / "30_timeseries_multidim" / "daily_kpi_multidim.csv", rows)

# ── Run ───────────────────────────────────────────────────────────
if __name__ == "__main__":
    print("正在生成交叉表测试数据集...")
    gen_sales_pivot()
    gen_product_hierarchy()
    gen_hr_crosstab()
    gen_cohort_matrix()
    gen_timeseries_multidim()
    print("完成。")
