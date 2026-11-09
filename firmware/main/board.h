// Waveshare ESP32-S3-Touch-LCD-4B pin map and board helpers.
#pragma once

#include "driver/gpio.h"
#include "driver/i2c_master.h"
#include "esp_io_expander.h"

#define BOARD_LCD_H_RES        480
#define BOARD_LCD_V_RES        480

// Shared I2C bus: TCA9554, GT911, PCF85063, QMI8658, AXP2101, ES8311/ES7210
#define BOARD_I2C_SDA          GPIO_NUM_47
#define BOARD_I2C_SCL          GPIO_NUM_48
#define BOARD_I2C_HZ           400000

// RGB panel (ST7701, RGB565 over 16 data lines)
#define BOARD_LCD_PCLK         GPIO_NUM_9
#define BOARD_LCD_DE           GPIO_NUM_17
#define BOARD_LCD_HSYNC        GPIO_NUM_46
#define BOARD_LCD_VSYNC        GPIO_NUM_3
#define BOARD_LCD_BL           GPIO_NUM_4
// D0..D15: B1..B5, G0..G5, R1..R5
#define BOARD_LCD_DATA_PINS { 40, 41, 42, 2, 1, 21, 8, 18, 45, 38, 39, 10, 11, 12, 13, 14 }

#define BOARD_BOOT_BTN         GPIO_NUM_0

// Shared duplex audio clocks, Waveshare ESP32-S3-Touch-LCD-4B BSP pin map.
#define BOARD_I2S_MCLK         GPIO_NUM_5
#define BOARD_I2S_BCLK         GPIO_NUM_16
#define BOARD_I2S_WS           GPIO_NUM_7
#define BOARD_I2S_DOUT         GPIO_NUM_6
#define BOARD_I2S_DIN          GPIO_NUM_15

// TCA9554 @0x20 pins
#define EXIO_LCD_CS            IO_EXPANDER_PIN_NUM_0
#define EXIO_LCD_SDA           IO_EXPANDER_PIN_NUM_1
#define EXIO_LCD_SCL           IO_EXPANDER_PIN_NUM_2
#define EXIO_PA_EN             IO_EXPANDER_PIN_NUM_3
#define EXIO_TP_RST            IO_EXPANDER_PIN_NUM_5
#define EXIO_TP_INT            IO_EXPANDER_PIN_NUM_6
#define EXIO_LCD_RST           IO_EXPANDER_PIN_NUM_7

esp_err_t board_init(void);
i2c_master_bus_handle_t board_i2c_bus(void);
esp_io_expander_handle_t board_expander(void);

void board_lcd_reset(void);
void board_touch_reset(void);

// 0..100
void board_backlight_set(int percent);
bool board_boot_pressed(void);
esp_err_t board_audio_amp(bool enabled);
