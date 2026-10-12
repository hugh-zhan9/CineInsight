import Foundation
import NaturalLanguage
let e=NLEmbedding.sentenceEmbedding(for:.simplifiedChinese)!
let query="一个消防栓坐在田野的草地上。"
for text in ["消防栓，草地，户外。","米饭，碗，食品。","草地上的红色消防栓。","这是一张结构、消防栓、户外、土地、草、天空、蓝天、植物的图片。"] {
 print(text,e.distance(between:query,and:text))
}
