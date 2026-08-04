
import asyncio
import re
import sys
import os
from datetime import datetime
from playwright.async_api import async_playwright, BrowserContext, Page

try:
    import hcaptcha_challenger as solver
    HCAPTCHA_AVAILABLE = True
except ImportError:
    HCAPTCHA_AVAILABLE = False
    print("⚠️  hcaptcha-challenger 未安装，遇到 hCaptcha 需要手动处理")
    print("安装命令: pip install -U hcaptcha-challenger")


class DeepSeekAutoRegisterV3:
    def __init__(self):
        self.email = None
        self.password = None
        self.verification_code = None
        self.accounts_file = os.path.join(os.path.dirname(__file__), "registered_accounts.txt")
    
    def extract_password_from_email(self, email: str) -> str:
        return email.split("@")[0]
    
    def save_account(self):
        timestamp = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
        account_info = (
            f"{'=' * 70}\n"
            f"注册时间: {timestamp}\n"
            f"邮箱: {self.email}\n"
            f"密码: {self.password}\n"
            f"{'=' * 70}\n\n"
        )
        
        with open(self.accounts_file, "a", encoding="utf-8") as f:
            f.write(account_info)
        
        print(f"\n✅ 账号信息已保存到: {self.accounts_file}")
    
    async def solve_hcaptcha(self, page: Page):
        if not HCAPTCHA_AVAILABLE:
            print("\n⚠️  检测到 hCaptcha，请手动完成验证，然后按回车继续...")
            input()
            return True
        
        try:
            print("\n🤖 正在自动解决 hCaptcha...")
            
            agent = solver.AgentV(page=page)
            await agent.robotic_arm.click_checkbox()
            await agent.wait_for_challenge()
            
            result = await agent.execute()
            
            if result:
                print("✅ hCaptcha 自动解决成功！")
                return True
            else:
                print("⚠️  hCaptcha 自动解决失败，请手动完成，然后按回车继续...")
                input()
                return True
        except Exception as e:
            print(f"⚠️  自动解决 hCaptcha 出错: {e}")
            print("请手动完成验证，然后按回车继续...")
            input()
            return True
    
    async def get_temp_email(self, page: Page) -> str:
        print("正在访问临时邮箱网站...")
        await page.goto("https://www.emailtick.com/zh", wait_until="domcontentloaded", timeout=60000)
        await asyncio.sleep(3)
        
        email_element = await page.query_selector('input[value*="@gmail.com"]')
        if email_element:
            self.email = await email_element.get_attribute("value")
            self.password = self.extract_password_from_email(self.email)
            print(f"✅ 获取到临时邮箱: {self.email}")
            print(f"✅ 密码: {self.password}")
            return self.email
        else:
            raise Exception("无法获取邮箱地址")
            
    async def wait_for_verification_code(self, page: Page) -> str:
        print("\n等待接收验证码...")
        
        max_attempts = 40
        for attempt in range(max_attempts):
            print(f"检查邮件 ({attempt + 1}/{max_attempts})...", end="\r")
            
            content = await page.content()
            
            code_match = re.search(r'(\d{6})', content)
            if code_match:
                self.verification_code = code_match.group(1)
                print(f"\n✅ 获取到验证码: {self.verification_code}")
                return self.verification_code
                
            try:
                refresh_btn = await page.query_selector('a:has-text("刷新"), a:has-text("检查邮件"), a:has-text("检查")')
                if refresh_btn:
                    await refresh_btn.click()
            except:
                pass
                
            await asyncio.sleep(3)
                
        print()
        raise Exception("超时未收到验证码")
        
    async def register_deepseek(self, context: BrowserContext):
        print("\n开始注册 DeepSeek...")
        
        page = await context.new_page()
        
        print("访问注册页面...")
        await page.goto("https://chat.deepseek.com/sign_up", wait_until="domcontentloaded", timeout=60000)
        await asyncio.sleep(4)
        
        print("填写邮箱...")
        email_input = await page.wait_for_selector('input[placeholder*="邮箱"]', timeout=30000)
        await email_input.fill(self.email)
        await asyncio.sleep(1)
        
        print("填写密码...")
        password_input = await page.wait_for_selector('input[placeholder*="密码"]', timeout=10000)
        await password_input.fill(self.password)
        await asyncio.sleep(1)
        
        print("确认密码...")
        confirm_inputs = await page.query_selector_all('input[placeholder*="密码"]')
        if len(confirm_inputs) >= 2:
            await confirm_inputs[1].fill(self.password)
            
        await asyncio.sleep(2)
        
        print("点击发送验证码...")
        send_btn = await page.wait_for_selector('button:has-text("发送验证码")', timeout=10000)
        
        try:
            await send_btn.click()
        except:
            await self.solve_hcaptcha(page)
            await asyncio.sleep(1)
            await send_btn.click()
        
        print("✅ 已点击发送验证码")
        
        print("\n等待验证码邮件...")
        email_page = context.pages[0]
        await email_page.bring_to_front()
        await self.wait_for_verification_code(email_page)
        
        await page.bring_to_front()
        
        print("填写验证码...")
        code_input = await page.wait_for_selector('input[placeholder*="验证码"]', timeout=10000)
        await code_input.fill(self.verification_code)
        
        await asyncio.sleep(2)
        
        print("点击注册...")
        register_btn = await page.wait_for_selector('button:has-text("注册")', timeout=10000)
        
        try:
            await register_btn.click()
        except:
            await self.solve_hcaptcha(page)
            await asyncio.sleep(1)
            await register_btn.click()
        
        print("✅ 注册请求已发送")
        await asyncio.sleep(8)
        
        await page.screenshot(path="registration_result.png", full_page=True)
        print("\n✅ 注册完成！结果已保存到 registration_result.png")
        print(f"邮箱: {self.email}")
        print(f"密码: {self.password}")
        
        self.save_account()
        
    async def run(self):
        print("=" * 70)
        print("          DeepSeek 自动注册工具 v3.0 (集成 hCaptcha 自动解决)")
        print("=" * 70)
        
        if HCAPTCHA_AVAILABLE:
            print("\n✅ hcaptcha-challenger 已加载，将尝试自动解决 hCaptcha")
        else:
            print("\n⚠️  hcaptcha-challenger 未安装，遇到 hCaptcha 需要手动处理")
        
        async with async_playwright() as p:
            print("\n启动浏览器...")
            browser = await p.chromium.launch(
                headless=False,
                args=[
                    '--disable-blink-features=AutomationControlled',
                    '--disable-dev-shm-usage',
                    '--no-sandbox',
                ]
            )
            
            context = await browser.new_context(
                viewport={'width': 1920, 'height': 1080},
                user_agent='Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36',
                locale='zh-CN'
            )
            
            await context.add_init_script("""
                Object.defineProperty(navigator, 'webdriver', {
                    get: () => undefined
                });
                Object.defineProperty(navigator, 'plugins', {
                    get: () => [1, 2, 3, 4, 5],
                });
                Object.defineProperty(navigator, 'languages', {
                    get: () => ['zh-CN', 'zh', 'en'],
                });
            """)
            
            try:
                print("\n--- 第1步：获取临时邮箱 ---")
                email_page = await context.new_page()
                await self.get_temp_email(email_page)
                
                print("\n--- 第2步：注册 DeepSeek ---")
                await self.register_deepseek(context)
                
            except Exception as e:
                print(f"\n❌ 发生错误: {e}")
                import traceback
                traceback.print_exc()
            
            print("\n按 Ctrl+C 退出，或等待 60 秒后自动关闭...")
            try:
                await asyncio.sleep(60)
            except:
                pass
                
            await browser.close()


if __name__ == "__main__":
    register = DeepSeekAutoRegisterV3()
    try:
        asyncio.run(register.run())
    except KeyboardInterrupt:
        print("\n用户中断")

