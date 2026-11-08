#include "board.h"

#include "driver/ledc.h"
#include "esp_check.h"
#include "esp_io_expander_tca9554.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

static const char *TAG = "board";

static i2c_master_bus_handle_t s_i2c;
static esp_io_expander_handle_t s_exp;

#define BL_LEDC_TIMER   LEDC_TIMER_0
#define BL_LEDC_CH      LEDC_CHANNEL_1
#define BL_LEDC_RES     LEDC_TIMER_10_BIT
// LEDC duty accepts [0, 2**duty_resolution] (inclusive): the top value is a
// documented special case that outputs a full period with no gap, unlike
// (2**duty_resolution)-1 which still leaves a ~0.1% glitch each cycle. Using
// it here is what makes "fully off" and "fully on" glitch-free below.
#define BL_LEDC_MAX_DUTY (1u << BL_LEDC_RES)

static esp_err_t backlight_init(void)
{
    const ledc_timer_config_t tcfg = {
        .speed_mode = LEDC_LOW_SPEED_MODE,
        .duty_resolution = BL_LEDC_RES,
        .timer_num = BL_LEDC_TIMER,
        .freq_hz = 20000,
        .clk_cfg = LEDC_AUTO_CLK,
    };
    ESP_RETURN_ON_ERROR(ledc_timer_config(&tcfg), TAG, "ledc timer");
    // Idle (pre-board_backlight_set) duty keeps the active-low backlight off
    // so the panel doesn't flash full-bright with garbage data before init.
    const ledc_channel_config_t ccfg = {
        .gpio_num = BOARD_LCD_BL,
        .speed_mode = LEDC_LOW_SPEED_MODE,
        .channel = BL_LEDC_CH,
        .timer_sel = BL_LEDC_TIMER,
        .duty = BL_LEDC_MAX_DUTY,
    };
    return ledc_channel_config(&ccfg);
}

// The backlight driver on this board is active-low: more time HIGH on
// BOARD_LCD_BL turns the backlight more OFF, more time LOW turns it more ON.
// Invert the duty so the API stays intuitive: 100 is brightest, 0 is off.
void board_backlight_set(int percent)
{
    if (percent < 0) percent = 0;
    if (percent > 100) percent = 100;
    uint32_t duty = BL_LEDC_MAX_DUTY - BL_LEDC_MAX_DUTY * (uint32_t)percent / 100;
    ledc_set_duty(LEDC_LOW_SPEED_MODE, BL_LEDC_CH, duty);
    ledc_update_duty(LEDC_LOW_SPEED_MODE, BL_LEDC_CH);
}

esp_err_t board_init(void)
{
    const i2c_master_bus_config_t bus_cfg = {
        .i2c_port = I2C_NUM_0,
        .sda_io_num = BOARD_I2C_SDA,
        .scl_io_num = BOARD_I2C_SCL,
        .clk_source = I2C_CLK_SRC_DEFAULT,
        .glitch_ignore_cnt = 7,
        .flags.enable_internal_pullup = true,
    };
    ESP_RETURN_ON_ERROR(i2c_new_master_bus(&bus_cfg, &s_i2c), TAG, "i2c bus");

    ESP_RETURN_ON_ERROR(esp_io_expander_new_i2c_tca9554(s_i2c, ESP_IO_EXPANDER_I2C_TCA9554_ADDRESS_000, &s_exp),
                        TAG, "tca9554");
    const uint32_t outs = EXIO_LCD_CS | EXIO_LCD_SDA | EXIO_LCD_SCL | EXIO_PA_EN | EXIO_TP_RST | EXIO_LCD_RST;
    esp_io_expander_set_dir(s_exp, outs, IO_EXPANDER_OUTPUT);
    esp_io_expander_set_level(s_exp, EXIO_LCD_CS | EXIO_LCD_RST | EXIO_TP_RST, 1);
    esp_io_expander_set_level(s_exp, EXIO_PA_EN, 0); // speaker amp off

    const gpio_config_t btn = {
        .pin_bit_mask = 1ULL << BOARD_BOOT_BTN,
        .mode = GPIO_MODE_INPUT,
        .pull_up_en = GPIO_PULLUP_ENABLE,
    };
    gpio_config(&btn);

    ESP_RETURN_ON_ERROR(backlight_init(), TAG, "backlight");
    return ESP_OK;
}

i2c_master_bus_handle_t board_i2c_bus(void) { return s_i2c; }
esp_io_expander_handle_t board_expander(void) { return s_exp; }

void board_lcd_reset(void)
{
    esp_io_expander_set_level(s_exp, EXIO_LCD_RST, 0);
    vTaskDelay(pdMS_TO_TICKS(10));
    esp_io_expander_set_level(s_exp, EXIO_LCD_RST, 1);
    vTaskDelay(pdMS_TO_TICKS(120));
}

// GT911 latches its I2C address from INT during reset: INT low -> 0x5D.
void board_touch_reset(void)
{
    esp_io_expander_set_dir(s_exp, EXIO_TP_INT, IO_EXPANDER_OUTPUT);
    esp_io_expander_set_level(s_exp, EXIO_TP_INT, 0);
    esp_io_expander_set_level(s_exp, EXIO_TP_RST, 0);
    vTaskDelay(pdMS_TO_TICKS(20));
    esp_io_expander_set_level(s_exp, EXIO_TP_RST, 1);
    vTaskDelay(pdMS_TO_TICKS(60));
    esp_io_expander_set_dir(s_exp, EXIO_TP_INT, IO_EXPANDER_INPUT);
    vTaskDelay(pdMS_TO_TICKS(50));
}

bool board_boot_pressed(void)
{
    return gpio_get_level(BOARD_BOOT_BTN) == 0;
}
